using System.Diagnostics;
using System.Text.Json;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.WinForms;

namespace Cage.Windows;

internal sealed class MainWindow : Form
{
    private WebView2 web = new() { Dock = DockStyle.Fill, Visible = false };
    private readonly NativeInput nativeInput;
    private readonly Label message = new() { AutoSize = true, MaximumSize = new Size(720, 0), Text = InterfaceLanguage.Text("正在启动 Cage 工作台…"), Margin = new Padding(12) };
    private readonly FlowLayoutPanel status = new() { Dock = DockStyle.Fill, FlowDirection = FlowDirection.TopDown, Padding = new Padding(48), WrapContents = false };
    private readonly Button retry = new() { Text = InterfaceLanguage.Text("重试连接"), AutoSize = true, Visible = false };
    private readonly Button existing = new() { Text = InterfaceLanguage.Text("打开现有环境保存文件"), AutoSize = true, Visible = false };
    private readonly Button complete = new() { Text = InterfaceLanguage.Text("保存后停止环境并完成更新"), AutoSize = true, Visible = false };
    private readonly NotifyIcon tray;
    private readonly AppUpdater updater;
    private int expectedPort;
    private bool controllerNeedsUpdate, allowQuit, bootstrapping, initializing, webFailed, quitProbeInFlight;
    private bool completingSmoke;
    // Bridge requests still running on the UI thread, such as refreshSession's "desktop".
    private int nativeRequests;
    private string smokePhase = "starting";
    private readonly CancellationTokenSource lifetime = new();
    private readonly Dictionary<string, (string[] Paths, DateTime Expires)> fileSelections = new();
    private (string TerminalID, string ConnectionID)? pendingNotification;
    // The tray balloon reports clicks without saying which balloon was clicked.
    // Once two targets are outstanding, a click only brings Cage forward.
    private bool pendingNotificationAmbiguous;

    public MainWindow()
    {
        nativeInput = new NativeInput(value => Script("window.dispatchEvent(new CustomEvent('cage-native-input',{detail:" + JsonSerializer.Serialize(value) + "}));"));
        Activated += (_, _) => PublishFocus(true);
        Deactivate += (_, _) => { PublishFocus(false); Script("window.dispatchEvent(new Event('cage-window-deactivated'));"); };
        AutoScaleMode = AutoScaleMode.Dpi; AutoScaleDimensions = new SizeF(96, 96);
        Text = "Cage";
        chrome = WindowChrome.SystemDefault();
        StartPosition = FormStartPosition.CenterScreen;
        BackColor = chrome.Background;
        message.ForeColor = chrome.Foreground;
        Icon = Icon.ExtractAssociatedIcon(Environment.ProcessPath!) ?? SystemIcons.Application;
        status.Controls.AddRange([message, retry, existing, complete]);
        Controls.Add(web); Controls.Add(status);
        Controls.Add(nativeInput);
        retry.Click += async (_, _) => {
            if (webFailed || web.CoreWebView2 == null) await Initialize(true); else await Bootstrap();
        };
        existing.Click += async (_, _) => await Bootstrap("--existing");
        complete.Click += async (_, _) => {
            if (ConfirmStop()) await Bootstrap("--complete-update");
        };
        var trayMenu = new TrayMenu();
        AddTrayItem("打开 Cage", TrayIconKind.Open, (_, _) => RestoreWindow());
        AddTrayItem("重新加载界面", TrayIconKind.CheckUpdates, async (_, _) => await ReloadWorkbench());
        AddTrayItem("检查应用更新…", TrayIconKind.CheckUpdates, (_, _) => updater!.Handle("check"));
        AddTrayItem("关于 Cage", TrayIconKind.About, (_, _) => MessageBox.Show(this,
            $"Cage {AppUpdater.Version}\n" + InterfaceLanguage.Text("本机隔离编码工作区"),
            InterfaceLanguage.Text("关于 Cage")));
        trayMenu.Items.Add(new ToolStripSeparator());
        AddTrayItem("退出 Cage…", TrayIconKind.Quit, async (_, _) => await RequestQuit(), destructive: true);
        trayMenu.Opening += (_, _) => {
            foreach (ToolStripItem item in trayMenu.Items)
                if (item.Tag is TrayItemMetadata meta) item.Text = InterfaceLanguage.Text(meta.SourceKey);
            (chrome ?? WindowChrome.SystemDefault()).ApplyMenu(trayMenu);
        };
        tray = new NotifyIcon { Icon = Icon, Text = "Cage", Visible = true, ContextMenuStrip = trayMenu };
        tray.DoubleClick += (_, _) => RestoreWindow();
        tray.BalloonTipClicked += (_, _) => {
            RestoreWindow();
            var target = pendingNotification;
            var ambiguous = pendingNotificationAmbiguous;
            pendingNotification = null;
            pendingNotificationAmbiguous = false;
            if (!ambiguous && target != null && target.Value.TerminalID.Length > 0)
                Script("window.dispatchEvent(new CustomEvent('cage-notification-click',{detail:" + JsonSerializer.Serialize(new { terminal_id = target.Value.TerminalID, connection_id = target.Value.ConnectionID }) + "}));");
        };
        updater = new AppUpdater(PublishUpdate, () => { allowQuit = true; Close(); }, () => _ = Bootstrap(), report: message => _ = Backend.Report(message));
        Shown += async (_, _) => {
            if (Program.SmokeOutput != null) _ = SmokeDeadline();
            await Initialize();
        };
        Resize += (_, _) => PublishVisibility(); VisibleChanged += (_, _) => PublishVisibility();
        FormClosing += (_, e) => {
            SaveWindowPlacement();
            if (!allowQuit && e.CloseReason is not (CloseReason.WindowsShutDown or CloseReason.TaskManagerClosing)) { e.Cancel = true; Hide(); }
        };
        FormClosed += (_, _) => { lifetime.Cancel(); updater.Dispose(); tray.Dispose(); trayMenu.Dispose(); web.Dispose(); };
        void AddTrayItem(string source, TrayIconKind icon, EventHandler action, bool destructive = false) =>
            trayMenu.Items.Add(InterfaceLanguage.Text(source), null, action).Tag =
                new TrayItemMetadata(source, icon, destructive);
    }

    protected override void OnLoad(EventArgs e)
    {
        base.OnLoad(e);
        WindowPlacement? saved = null;
        try { saved = JsonSerializer.Deserialize<WindowPlacement>(File.ReadAllText(Path.Combine(Backend.Root, "window-placement.json"))); }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException or JsonException) { }
        var work = Screen.FromHandle(Handle).WorkingArea;
        MinimumSize = WindowLayout.Minimum(work, DeviceDpi);
        StartPosition = FormStartPosition.Manual;
        Bounds = WindowLayout.Bounds(work, DeviceDpi, saved);
        if (saved?.Maximized == true) WindowState = FormWindowState.Maximized;
    }

    private void SaveWindowPlacement()
    {
        var bounds = WindowState == FormWindowState.Normal ? Bounds : RestoreBounds;
        if (bounds.Width <= 0 || bounds.Height <= 0) return;
        try
        {
            Directory.CreateDirectory(Backend.Root);
            File.WriteAllText(Path.Combine(Backend.Root, "window-placement.json"),
                JsonSerializer.Serialize(new WindowPlacement(bounds.X, bounds.Y, bounds.Width, bounds.Height, DeviceDpi,
                    WindowState == FormWindowState.Maximized)));
        }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException) { }
    }

    protected override bool ProcessCmdKey(ref Message msg, Keys keyData)
    {
        var action = keyData switch {
            Keys.Control | Keys.O => "openProject",
            Keys.Control | Keys.T => "newTab",
            Keys.Control | Keys.Shift | Keys.Enter => "newTerminal",
            Keys.Control | Keys.W => "closeTab",
            Keys.Control | Keys.Oemcomma => "settings",
            _ => null,
        };
        if (action == null) return base.ProcessCmdKey(ref msg, keyData);
        Action(action); return true;
    }

    private WindowChrome? chrome;
    private bool hasWorkbenchAppearance;

    private void ApplyWindowChrome()
    {
        if (IsHandleCreated)
            chrome?.Apply(Handle, MainMenuStrip);
        if (tray?.ContextMenuStrip != null)
            chrome?.ApplyMenu(tray.ContextMenuStrip);
    }

    protected override void OnHandleCreated(EventArgs e)
    {
        base.OnHandleCreated(e);
        ApplyWindowChrome();
    }

    protected override void OnSystemColorsChanged(EventArgs e)
    {
        base.OnSystemColorsChanged(e);
        if (!hasWorkbenchAppearance)
        {
            chrome = WindowChrome.SystemDefault();
            BackColor = chrome.Background;
            message.ForeColor = chrome.Foreground;
        }
        ApplyWindowChrome();
    }

    private bool ConfirmStop() => MessageBox.Show(this,
        InterfaceLanguage.Text("请先保存编辑器中的文件并结束正在运行的工作。继续后 Cage 会停止环境、安装已验证的更新并重新启动。"),
        InterfaceLanguage.Text("保存并停止环境后安装重启"), MessageBoxButtons.OKCancel, MessageBoxIcon.Information) == DialogResult.OK;

    public void RestoreWindow() { Show(); if (WindowState == FormWindowState.Minimized) WindowState = FormWindowState.Normal; Activate(); PublishVisibility(); }
    private async Task ReloadWorkbench()
    {
        RestoreWindow();
        if (initializing || bootstrapping) return;
        _ = Backend.Report("Reloading workbench WebView");
        if (controllerNeedsUpdate) await Initialize(true, "--existing");
        else await Initialize(true);
    }
    private async Task RequestQuit()
    {
        RestoreWindow();
        if (!web.Visible || web.IsDisposed || web.CoreWebView2 == null || webFailed)
        {
            allowQuit = true; Close(); return;
        }
        if (quitProbeInFlight) return;
        quitProbeInFlight = true;
        var probedWeb = web;
        try
        {
            var probe = probedWeb.ExecuteScriptAsync("typeof window.cageNativeAction === 'function' ? (window.cageNativeAction('quit'), true) : false");
            _ = probe.ContinueWith(static t => _ = t.Exception, TaskContinuationOptions.OnlyOnFaulted);
            var completed = await Task.WhenAny(probe, Task.Delay(TimeSpan.FromMilliseconds(1500), lifetime.Token));
            if (allowQuit || IsDisposed || !ReferenceEquals(web, probedWeb)) return;
            if (completed == probe)
            {
                var result = await probe;
                if (result == "true") return;
                _ = Backend.Report("Workbench quit handoff returned " + result + "; closing desktop window");
            }
            else
            {
                _ = Backend.Report("Workbench did not respond to quit request within 1.5s; closing desktop window");
            }
        }
        catch (Exception error) when (error is InvalidOperationException or System.Runtime.InteropServices.COMException or OperationCanceledException)
        {
            if (allowQuit || IsDisposed || !ReferenceEquals(web, probedWeb)) return;
            _ = Backend.Report("Workbench quit handoff failed: " + error.Message);
        }
        finally { quitProbeInFlight = false; }
        allowQuit = true;
        Close();
    }
    private void Action(string name) => Script("window.cageNativeAction?.(" + JsonSerializer.Serialize(name) + ")");
    private void PublishFocus(bool focused) => Script($"window.cageWindowFocused = {(focused ? "true" : "false")}; window.dispatchEvent(new Event('cage-window-focus'));");
    private void PublishVisibility()
    {
        var visible = Visible && WindowState != FormWindowState.Minimized;
        Script($"window.cageWindowVisible = {(visible ? "true" : "false")}; window.dispatchEvent(new Event('cage-window-visibility'));");
        PublishFocus(visible && Form.ActiveForm == this);
    }
    private void PublishUpdate(Dictionary<string, object?> state) {
        if (state.TryGetValue("phase", out var phase) && phase as string == "failed") _ = Backend.Report("App update failed: " + state.GetValueOrDefault("message"));
        Script("window.dispatchEvent(new CustomEvent('cage-app-update',{detail:" + JsonSerializer.Serialize(state) + "}));");
    }
    private async void Script(string script)
    {
        if (web.IsDisposed || web.CoreWebView2 == null || IsDisposed) return;
        try { await web.ExecuteScriptAsync(script); }
        catch (InvalidOperationException) { }
        catch (System.Runtime.InteropServices.COMException) { }
    }
    private void Error(string text)
    {
        _ = Backend.Report(text);
        message.Text = text; status.Show(); status.BringToFront(); retry.Show(); web.Hide();
    }

    private async Task Initialize(bool recreate = false, params string[] bootstrapOptions)
    {
        if (initializing) return;
        initializing = true;
        smokePhase = "initializing_webview";
        retry.Enabled = false;
        try
        {
            if (recreate)
            {
                message.Text = InterfaceLanguage.Text("正在启动 Cage 工作台…");
                status.Show(); status.BringToFront();
                retry.Hide(); existing.Hide(); complete.Hide();
                Controls.Remove(web); web.Dispose();
                web = new WebView2 { Dock = DockStyle.Fill, Visible = false };
                Controls.Add(web); status.BringToFront();
            }
            var environment = await CoreWebView2Environment.CreateAsync(null, Path.Combine(Backend.Root, "webview2"));
            await web.EnsureCoreWebView2Async(environment);
            smokePhase = "webview_initialized";
            var core = web.CoreWebView2;
            core.Settings.AreDevToolsEnabled = false; core.Settings.AreDefaultContextMenusEnabled = false;
            core.Settings.IsStatusBarEnabled = false; core.Settings.IsZoomControlEnabled = false;
            core.Settings.AreBrowserAcceleratorKeysEnabled = false;
            core.Settings.IsPasswordAutosaveEnabled = false; core.Settings.IsGeneralAutofillEnabled = false;
            core.NavigationStarting += (_, e) => { if (!UpdatePolicy.IsWorkbench(e.Uri, expectedPort)) e.Cancel = true; };
            core.NewWindowRequested += (_, e) => e.Handled = true;
            core.DownloadStarting += (_, e) => e.Cancel = true;
            core.PermissionRequested += (_, e) => {
                e.State = e.PermissionKind == CoreWebView2PermissionKind.Microphone &&
                    UpdatePolicy.AllowMicrophonePermission(e.Uri, expectedPort)
                    ? CoreWebView2PermissionState.Allow
                    : CoreWebView2PermissionState.Deny;
            };
            core.WebMessageReceived += Receive;
            core.ProcessFailed += (_, _) => { webFailed = true; Error(InterfaceLanguage.Text("工作台显示进程已退出。后台任务保留，可重试连接。")); };
            core.NavigationCompleted += (_, e) => {
                if (!e.IsSuccess) {
                    if (e.WebErrorStatus == CoreWebView2WebErrorStatus.OperationCanceled) return;
                    Error(e.WebErrorStatus.ToString() switch {
                        "Timeout" => InterfaceLanguage.Text("打开工作台失败：等待本机后台响应超时。请点击“重试连接”；仍失败时请保留此错误提示并反馈给维护者。后台任务不会因此自动结束。"),
                        "CannotConnect" => InterfaceLanguage.Text("打开工作台失败：本机后台没有接受连接，可能尚未启动或已经退出。请点击“重试连接”；仍失败时请保留此错误提示并反馈给维护者。后台任务不会因此自动结束。"),
                        "ConnectionAborted" or "ConnectionReset" => InterfaceLanguage.Text("打开工作台失败：本机工作台连接在加载完成前中断。请点击“重试连接”；仍失败时请保留此错误提示并反馈给维护者。后台任务不会因此自动结束。"),
                        _ => InterfaceLanguage.Format("打开工作台失败：内置网页组件未能加载本机工作台（错误代码 {0}）。请点击“重试连接”；仍失败时请保留此错误提示并反馈给维护者。后台任务不会因此自动结束。", e.WebErrorStatus.ToString()),
                    }); return;
                }
                status.Hide(); web.Show(); web.Focus(); PublishVisibility();
            };
            await core.AddScriptToExecuteOnDocumentCreatedAsync(BridgeScript);
            if (Program.SmokeOutput != null) await core.AddScriptToExecuteOnDocumentCreatedAsync(SmokeDiagnosticsScript);
            webFailed = false;
            await Bootstrap(bootstrapOptions);
        }
        catch (WebView2RuntimeNotFoundException)
        {
            Error(InterfaceLanguage.Text("缺少 Microsoft Edge WebView2 Runtime。请运行 Cage 安装程序安装随附运行时后重试。"));
            webFailed = true;
        }
        catch (Exception error) { webFailed = true; Error(InterfaceLanguage.Format("无法初始化工作台：{0}", error.Message)); }
        finally { initializing = false; retry.Enabled = true; }
    }

    private async Task SmokeDeadline()
    {
        try { await Task.Delay(TimeSpan.FromSeconds(120), lifetime.Token); await CompleteSmoke(false, "工作台未在自检期限内确认就绪"); }
        catch (OperationCanceledException) { }
    }

    private async Task CompleteSmoke(bool nativeBridgeReady = true, string? failure = null)
    {
        if (Program.SmokeOutput == null || completingSmoke) return;
        completingSmoke = true;
        // Navigation hides the status panel but keeps its last text. Report it
        // only while visible, so a recovered error never enters a success report.
        // A hidden window hides every child, so keep the old text there.
        var nativeStatus = status.Visible || !Visible ? message.Text : "";
        JsonElement? page = null;
        var pageURL = web.Source?.GetLeftPart(UriPartial.Path);
        var environment = web.CoreWebView2?.Environment;
        var webviewVersion = environment?.BrowserVersionString;
        using var shutdown = CancellationTokenSource.CreateLinkedTokenSource(lifetime.Token);
        shutdown.CancelAfter(TimeSpan.FromSeconds(20));
        var errors = new List<string>();
        int? prepareExit = null;
        string? prepareStatus = null;
        string? prepareMessage = null;
        if (failure != null) errors.Add(failure);
        try
        {
            if (!nativeBridgeReady && web.CoreWebView2 != null)
            {
                try
                {
                    var snapshot = await web.CoreWebView2.ExecuteScriptAsync("({title:document.title,ready:document.readyState,text:(document.body?.innerText||'').slice(0,4096),errors:window.__cageSmokeErrors||[]})");
                    page = JsonDocument.Parse(snapshot).RootElement.Clone();
                    await using var capture = File.Create(Path.ChangeExtension(Program.SmokeOutput, ".png"));
                    await web.CoreWebView2.CapturePreviewAsync(CoreWebView2CapturePreviewImageFormat.Png, capture);
                }
                catch (Exception error) { errors.Add("无法保存工作台画面：" + error.Message); }
            }
            // The page can still refresh its session through the bridge, which runs
            // "desktop" and may start a new connection service while prepare-update
            // retires the old one. New requests are refused once completingSmoke is
            // set; let the ones already running finish first.
            if (nativeBridgeReady) smokePhase = "waiting_native_requests";
            while (nativeRequests > 0) await Task.Delay(50, shutdown.Token);
            var controllers = Backend.CaptureSmokeControllers();
            try
            {
                if (nativeBridgeReady) smokePhase = "stopping_backend";
                var (exit, data) = await Backend.Json(["prepare-update", "--json"], shutdown.Token, TimeSpan.FromSeconds(20));
                prepareExit = exit;
                prepareStatus = data.Text("status") is "prepared" or "stop_required" or "error" ? data.Text("status") : "unexpected";
                // This report is uploaded by CI. Keep the shutdown error but
                // remove URL queries/fragments and bound its size.
                prepareMessage = System.Text.RegularExpressions.Regex.Replace(data.Text("message"), @"[?#][^\s""'<>)]*", "[redacted]");
                if (prepareMessage.Length > 768) prepareMessage = prepareMessage[..768];
                if (exit != 0 || data.Text("status") != "prepared") throw new IOException("自检结束时无法确认后台已停止");
                await Task.WhenAll(controllers.Select(process => process.WaitForExitAsync(shutdown.Token)));
            }
            finally { foreach (var process in controllers) process.Dispose(); }
        }
        catch (Exception error) { errors.Add(error.Message); }
        // WebView2 releases its processes asynchronously after Dispose. Its
        // browser can retain the app's working directory until this event,
        // though the exit event can race with Dispose() or be missed if the
        // browser process already exited. Wait on both the event and the OS
        // process handle so an already-exited browser never fails the smoke run.
        if (environment != null)
        {
            var browserPid = web.CoreWebView2?.BrowserProcessId ?? 0u;
            var exited = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
            void BrowserExited(object? sender, CoreWebView2BrowserProcessExitedEventArgs args) => exited.TrySetResult();
            environment.BrowserProcessExited += BrowserExited;
            try
            {
                if (nativeBridgeReady && errors.Count == 0) smokePhase = "stopping_browser";
                web.Dispose();
                Process? browserProcess = null;
                if (browserPid != 0)
                {
                    try { browserProcess = Process.GetProcessById((int)browserPid); }
                    catch (ArgumentException) { exited.TrySetResult(); }
                    catch (InvalidOperationException) { exited.TrySetResult(); }
                }
                try
                {
                    if (browserProcess == null)
                    {
                        await exited.Task.WaitAsync(shutdown.Token);
                    }
                    else if (!browserProcess.HasExited)
                    {
                        var completed = await Task.WhenAny(exited.Task, browserProcess.WaitForExitAsync(shutdown.Token)).WaitAsync(shutdown.Token);
                        await completed;
                    }
                }
                finally { browserProcess?.Dispose(); }
            }
            catch (Exception error) { errors.Add("自检浏览器尚未退出：" + error.Message); }
            finally { environment.BrowserProcessExited -= BrowserExited; }
        }
        try
        {
            if (nativeBridgeReady && errors.Count == 0) smokePhase = "completed";
            File.WriteAllText(Program.SmokeOutput, JsonSerializer.Serialize(new {
                success = nativeBridgeReady && errors.Count == 0, nativeBridgeReady, version = AppUpdater.Version,
                phase = smokePhase, nativeStatus, prepareExit, prepareStatus, prepareMessage, errors, page,
                pageURL, webviewVersion,
                windowBounds = Bounds, workingArea = Screen.FromHandle(Handle).WorkingArea, deviceDpi = DeviceDpi,
                nativeMenuBar = MainMenuStrip != null,
            }));
        }
        catch (Exception error) { Error(error.Message); }
        finally { allowQuit = true; BeginInvoke(Close); }
    }

    private async Task Bootstrap(params string[] options)
    {
        if (bootstrapping || web.CoreWebView2 == null) return;
        bootstrapping = true;
        message.Text = InterfaceLanguage.Text("正在连接 Cage 工作台…"); status.Show(); status.BringToFront();
        retry.Hide(); existing.Hide(); complete.Hide();
        try
        {
            smokePhase = "starting_backend";
            var (exit, data) = await Backend.Json(["desktop", "--json", ..options], lifetime.Token);
            smokePhase = "backend_returned";
            if (exit != 0) throw new IOException(data.Text("message") is { Length: > 0 } detail ? detail : InterfaceLanguage.Text("工作台未能启动"));
            controllerNeedsUpdate = data.Text("status") != "ready";
            if (data.Text("status") == "upgrade_pending")
            {
                message.Text = data.Text("message"); existing.Show(); complete.Show(); return;
            }
            var entry = UpdatePolicy.Entry(data.Text("url")); expectedPort = entry.Port;
            smokePhase = "loading_workbench";
            Text = controllerNeedsUpdate ? InterfaceLanguage.Text("Cage（更新待完成）") : "Cage";
            web.CoreWebView2.Navigate(entry.AbsoluteUri);
        }
        catch (OperationCanceledException) { if (!lifetime.IsCancellationRequested) Error(InterfaceLanguage.Text("启动工作台失败：等待本机后台响应超时。请点击“重试连接”；仍失败时请保留此提示并反馈给维护者。")); }
        catch (Exception error) {
            _ = Backend.Report("Workbench startup failed: " + error);
            Error(error.Message.Any(c => c is >= '\u4E00' and <= '\u9FFF') ? error.Message : InterfaceLanguage.Format("启动工作台失败：本机后台未返回可识别的原因（错误代码 {0}）。请点击“重试连接”；仍失败时请保留此提示并反馈给维护者。", error.GetType().Name));
        }
        finally { bootstrapping = false; }
    }

    private object RememberFiles(string[] paths)
    {
        if (paths.Length is 0 or > 100) throw new InvalidDataException(InterfaceLanguage.Text("请选择 1 至 100 个文件"));
        foreach (var path in paths)
        {
            if (!Path.IsPathFullyQualified(path) || path.StartsWith(@"\\", StringComparison.Ordinal)) throw new InvalidDataException(InterfaceLanguage.Text("请选择本机文件"));
            var file = new FileInfo(path);
            if (!file.Exists || (file.Attributes & (FileAttributes.Directory | FileAttributes.ReparsePoint)) != 0)
                throw new InvalidDataException(InterfaceLanguage.Text("请选择普通文件，暂不支持文件夹或链接"));
            if (file.Length > 2L * 1024 * 1024 * 1024) throw new InvalidDataException(InterfaceLanguage.Text("每个文件不能超过 2 GB"));
        }
        var now = DateTime.UtcNow;
        foreach (var key in fileSelections.Where(item => item.Value.Expires <= now).Select(item => item.Key).ToArray()) fileSelections.Remove(key);
        if (fileSelections.Count >= 16) fileSelections.Remove(fileSelections.MinBy(item => item.Value.Expires).Key);
        var selection = Guid.NewGuid().ToString();
        fileSelections[selection] = (paths, now.AddMinutes(10));
        return new { fileSelection = selection, count = paths.Length };
    }

    private async void Receive(object? sender, CoreWebView2WebMessageReceivedEventArgs e)
    {
        // CoreWebView2.WebMessageReceived is top-document only. Frame messages
        // have a separate CoreWebView2Frame event, which this host never handles.
        if (!UpdatePolicy.IsWorkbench(e.Source, expectedPort)) return;
        string id = "";
        var counted = false;
        try
        {
            var message = e.WebMessageAsJson;
            using var envelope = JsonDocument.Parse(message);
            var body = envelope.RootElement;
            id = body.Text("id");
            if (!Guid.TryParse(id, out _)) return;
            if (completingSmoke)
            {
                if (!web.IsDisposed) web.CoreWebView2.PostWebMessageAsJson(JsonSerializer.Serialize(new { id, error = "自检正在结束，未执行新的原生操作" }));
                return;
            }
            nativeRequests++; counted = true;
            // Match the Linux bridge envelope limit. A 20 MiB PNG needs about
            // 27 MiB after Base64 encoding; retain the ID to reply on rejection.
            if (message.Length > 30 * 1024 * 1024) throw new InvalidDataException("请求内容过大，请减少图片或文本内容后重试");
            var request = body.GetProperty("request");
            object? result;
            switch (request.Text("action"))
            {
                case "remoteInput":
                    var accepted = nativeInput.ProcessRequest(request, web, web.ZoomFactor, out var returnedFocus);
                    result = request.TryGetProperty("returnFocus", out var restore) && restore.ValueKind == JsonValueKind.True
                        ? new { returnedFocus = accepted && returnedFocus } : accepted;
                    break;
                case "exportDiagnostics":
                    using (var picker = new SaveFileDialog { Title = InterfaceLanguage.Text("导出诊断包"), Filter = InterfaceLanguage.Text("诊断包 (*.zip)") + "|*.zip", FileName = "cage-diagnostics.zip", DefaultExt = "zip", AddExtension = true })
                    {
                        if (picker.ShowDialog(this) != DialogResult.OK) { result = new { canceled = true }; break; }
                        var connection = request.Text("connection_id");
                        var exported = await Backend.Json(["diagnostics", "--json", "--connection", string.IsNullOrEmpty(connection) ? "local" : connection, picker.FileName], lifetime.Token, TimeSpan.FromSeconds(60));
                        if (exported.Exit != 0) throw new IOException(exported.Data.Text("message"));
                        result = exported.Data;
                    }
                    break;
                case "workbenchReady":
                    if (controllerNeedsUpdate) throw new IOException("后台版本尚未同步，更新未完成");
                    updater.MarkReady(); result = true; _ = CompleteSmoke(); break;
                case "appUpdate": result = updater.Handle(request.Text("operation")); break;
                case "importTheme":
                    using (var picker = new OpenFileDialog { Filter = InterfaceLanguage.Text("Cage 主题 (*.json)") + "|*.json", Title = InterfaceLanguage.Text("导入主题"), Multiselect = false })
                    {
                        if (picker.ShowDialog(this) != DialogResult.OK) { result = ""; break; }
                        using var stream = File.OpenRead(picker.FileName);
                        if (stream.Length > 256 * 1024) throw new InvalidDataException("主题文件不能超过 256 KB");
                        using var reader = new StreamReader(stream, new System.Text.UTF8Encoding(false, true));
                        result = reader.ReadToEnd();
                    }
                    break;
                case "saveRecoveryKey":
                {
                    var key = request.Text("key");
                    if (key.Length != 71 || !System.Text.RegularExpressions.Regex.IsMatch(key, "^[A-F0-9]{8}(-[A-F0-9]{8}){7}$")) throw new InvalidDataException("恢复密钥格式无效");
                    using var picker = new SaveFileDialog { Filter = InterfaceLanguage.Text("文本文件 (*.txt)") + "|*.txt", Title = InterfaceLanguage.Text("保存账户恢复密钥"), FileName = "账户恢复密钥.txt", DefaultExt = "txt", AddExtension = true };
                    if (picker.ShowDialog(this) != DialogResult.OK) { result = false; break; }
                    try { File.WriteAllText(picker.FileName, key + "\n", new System.Text.UTF8Encoding(false)); result = true; }
                    catch { throw new IOException("恢复密钥未能保存，请检查保存位置的权限"); }
                    break;
                }
                case "exportTheme":
                    var themeText = request.Text("text");
                    if (System.Text.Encoding.UTF8.GetByteCount(themeText) > 256 * 1024) throw new InvalidDataException("主题文件不能超过 256 KB");
                    var themeDocument = JsonDocument.Parse(themeText).RootElement;
                    if (themeDocument.Text("format") != "cage-theme" || themeDocument.GetProperty("version").GetInt32() != 1) throw new InvalidDataException("主题文件格式无效");
                    using (var picker = new SaveFileDialog { Filter = InterfaceLanguage.Text("Cage 主题 (*.json)") + "|*.json", Title = InterfaceLanguage.Text("导出主题"), FileName = Path.GetFileName(request.Text("filename")), DefaultExt = "json", AddExtension = true })
                    {
                        if (picker.ShowDialog(this) != DialogResult.OK) { result = false; break; }
                        File.WriteAllText(picker.FileName, themeText, new System.Text.UTF8Encoding(false)); result = true;
                    }
                    break;
                case "shareLocalProject":
                {
                    var connection = request.Text("connection_id");
                    var accessMode = request.Text("mode");
                    if (connection.Length == 0 || accessMode is not ("read" or "write")) throw new InvalidDataException("请在云端工作台重新选择项目");
                    using var picker = new FolderBrowserDialog {
                        Description = InterfaceLanguage.Text(accessMode == "write" ? "云端 Cage 可以读取、修改和删除所选项目中的文件。" : "云端 Cage 可以读取所选项目中的文件。"),
                        UseDescriptionForTitle = true, ShowNewFolderButton = false
                    };
                    if (picker.ShowDialog(this) != DialogResult.OK) { result = new { canceled = true }; break; }
                    var response = await Backend.Json(["share-project", "--json", "--connection", connection, accessMode, picker.SelectedPath], lifetime.Token, TimeSpan.FromSeconds(90));
                    if (response.Exit != 0) throw new IOException(response.Data.Text("message"));
                    result = response.Data; break;
                }
                case "chooseFolder":
                    using (var picker = new FolderBrowserDialog { Description = InterfaceLanguage.Text(request.Text("purpose") == "environmentStorage" ? "请选择用于保存镜像和虚拟磁盘的空文件夹。" : "仅此文件夹会与 Cage 共享。请选择具体项目。"), UseDescriptionForTitle = true, ShowNewFolderButton = true })
                        result = picker.ShowDialog(this) == DialogResult.OK ? picker.SelectedPath : "";
                    break;
                case "clipboardRead": result = Clipboard.ContainsText() ? Clipboard.GetText() : ""; break;
                case "clipboardRevision": result = SharedClipboard.Revision; break;
                case "clipboardSyncCancel": SharedClipboard.Cancel(); result = true; break;
                case "clipboardSyncRead": result = await SharedClipboard.Read(request, lifetime.Token); break;
                case "clipboardSyncWrite": result = await SharedClipboard.Write(request, lifetime.Token); break;
                case "clipboardSyncRelease": result = await SharedClipboard.Release(request, lifetime.Token); break;
                case "desktopFileAction":
                {
                    var fileRequestID = request.Text("id");
                    if (!Guid.TryParse(fileRequestID, out _)) throw new InvalidDataException("文件打开标识无效");
                    var connection = request.Text("connection_id");
                    var response = await Backend.Json(["desktop-file", "--json", "--connection", string.IsNullOrEmpty(connection) ? "local" : connection, fileRequestID], lifetime.Token, TimeSpan.FromMinutes(30));
                    if (response.Exit != 0) throw new IOException(response.Data.Text("message"));
                    result = response.Data; break;
                }
                case "saveBrowserDownload":
                case "browserDownloadAction":
                {
                    var downloadID = request.Text("id");
                    if (!Guid.TryParse(downloadID, out _)) throw new InvalidDataException("下载标识无效");
                    var operation = request.Text("action") == "saveBrowserDownload" ? "save" : request.Text("operation");
                    if (operation is not ("save" or "open" or "reveal")) throw new InvalidDataException("下载操作无效");
                    var connection = request.Text("connection_id");
                    var arguments = new List<string> { "browser-file", "--json", "--connection", string.IsNullOrEmpty(connection) ? "local" : connection, operation, downloadID };
                    if (operation == "save")
                    {
                        var fileName = Path.GetFileName(request.Text("name"));
                        foreach (var character in Path.GetInvalidFileNameChars()) fileName = fileName.Replace(character, '_');
                        using var picker = new SaveFileDialog { Title = InterfaceLanguage.Text("将下载另存为到本机"), FileName = fileName, OverwritePrompt = true, AddExtension = false };
                        if (picker.ShowDialog(this) != DialogResult.OK) { result = new { canceled = true }; break; }
                        arguments.Add(picker.FileName);
                    }
                    var response = await Backend.Json(arguments.ToArray(), lifetime.Token, TimeSpan.FromMinutes(30));
                    if (response.Exit != 0) throw new IOException(response.Data.Text("message"));
                    result = response.Data; break;
                }
                case "clipboardReadContent":
                    if (Clipboard.ContainsFileDropList()) result = RememberFiles(Clipboard.GetFileDropList().Cast<string>().ToArray());
                    else if (SharedClipboard.RichText() is { } copiedText) result = new { text = copiedText };
                    else if (Clipboard.ContainsImage())
                    {
                        using var clipboardImage = Clipboard.GetImage() ?? throw new InvalidDataException("无法读取剪贴板图片");
                        if ((long)clipboardImage.Width * clipboardImage.Height > 40_000_000) throw new InvalidDataException("图片不能超过 4000 万像素");
                        using var imageStream = new MemoryStream();
                        clipboardImage.Save(imageStream, System.Drawing.Imaging.ImageFormat.Png);
                        if (imageStream.Length > 20 * 1024 * 1024) throw new InvalidDataException("图片不能超过 20 MB");
                        result = new { png = Convert.ToBase64String(imageStream.ToArray()) };
                    }
                    else result = new { text = Clipboard.ContainsText() ? Clipboard.GetText() : "" };
                    break;
                case "chooseGuestMedia":
                    using (var picker = new OpenFileDialog { Title = InterfaceLanguage.Text("选择 Windows 11 x64 安装镜像"), Filter = InterfaceLanguage.Text("Windows ISO (*.iso)") + "|*.iso", Multiselect = false })
                        result = picker.ShowDialog(this) == DialogResult.OK ? picker.FileName : "";
                    break;
                case "appReference":
                    var appReference = request.Text("kind") switch {
                        "source" => "https://github.com/Lee-xeo/Claude-Cage/commits/main",
                        "releases" => "https://github.com/Lee-xeo/Claude-Cage/releases",
                        _ => throw new InvalidDataException("未知的项目页面")
                    };
                    System.Diagnostics.Process.Start(new System.Diagnostics.ProcessStartInfo(appReference) { UseShellExecute = true });
                    result = true; break;
                case "guestInstallReference":
                    var reference = request.Text("kind") switch {
                        "windows-media" => "https://www.microsoft.com/software-download/windows11",
                        "editor-license" => "https://aka.ms/vscode-server-license",
                        _ => throw new InvalidDataException("未知的安装参考页面")
                    };
                    System.Diagnostics.Process.Start(new System.Diagnostics.ProcessStartInfo(reference) { UseShellExecute = true });
                    result = true; break;
                case "chooseUploadFiles":
                    using (var picker = new OpenFileDialog { Title = InterfaceLanguage.Text("选择本机文件"), Multiselect = request.TryGetProperty("multiple", out var multiple) && multiple.ValueKind == JsonValueKind.True, CheckFileExists = true })
                        result = picker.ShowDialog(this) == DialogResult.OK ? RememberFiles(picker.FileNames) : new { canceled = true };
                    break;
                case "uploadHostFiles":
                {
                    var target = request.Text("target");
                    var uploadID = request.Text("id");
                    if (target is not ("browser" or "desktop" or "terminal") || !(target == "terminal" ? System.Text.RegularExpressions.Regex.IsMatch(uploadID, "^[a-f0-9]{16}$") : Guid.TryParse(uploadID, out _))) throw new InvalidDataException("文件上传目标无效");
                    if (!fileSelections.Remove(request.Text("selection"), out var selected) || selected.Expires <= DateTime.UtcNow)
                        throw new InvalidDataException("文件选择已过期，请重新复制或选择文件");
                    var connection = request.Text("connection_id");
                    var arguments = new List<string> { "upload-files", "--json", "--connection", string.IsNullOrEmpty(connection) ? "local" : connection, target, uploadID };
                    arguments.AddRange(selected.Paths);
                    var response = await Backend.Json(arguments.ToArray(), lifetime.Token, TimeSpan.FromMinutes(30));
                    if (response.Exit != 0) throw new IOException(response.Data.Text("message"));
                    result = response.Data;
                    break;
                }
                case "clipboardWrite":
                    if (request.Text("text") is { Length: > 0 } text) Clipboard.SetText(text); else Clipboard.Clear();
                    result = true; break;
                case "refreshLanguage":
                    await InterfaceLanguage.Refresh();
                    retry.Text = InterfaceLanguage.Text("重试连接");
                    existing.Text = InterfaceLanguage.Text("打开现有环境保存文件");
                    complete.Text = InterfaceLanguage.Text("保存后停止环境并完成更新");
                    Text = controllerNeedsUpdate ? InterfaceLanguage.Text("Cage（更新待完成）") : "Cage";
                    result = true; break;
                case "appearance":
                    var mode = request.Text("mode"); var background = request.Text("background");
                    if (mode is not ("system" or "light" or "dark") || !WindowChrome.TryParseHexColor(background, out var bgColor))
                        throw new InvalidDataException("外观参数无效");
                    var foreground = request.Text("foreground");
                    BackColor = bgColor; web.DefaultBackgroundColor = BackColor;
                    var ink = WindowChrome.TryParseHexColor(foreground, out var fgColor)
                        ? fgColor : WindowChrome.ContrastingText(BackColor);
                    this.message.ForeColor = ink;
                    hasWorkbenchAppearance = true;
                    chrome = WindowChrome.FromAppearance(
                        BackColor,
                        ink,
                        request.Text("variant") == "dark",
                        menuBackground: request.Text("menuBackground"),
                        menuForeground: request.Text("menuForeground"),
                        muted: request.Text("muted"),
                        border: request.Text("border"),
                        hover: request.Text("hover"),
                        selected: request.Text("selected"),
                        danger: request.Text("danger"));
                    ApplyWindowChrome();
                    web.CoreWebView2.Profile.PreferredColorScheme = mode == "dark" ? CoreWebView2PreferredColorScheme.Dark : mode == "light" ? CoreWebView2PreferredColorScheme.Light : CoreWebView2PreferredColorScheme.Auto;
                    result = true; break;
                case "accountDevice":
                    if (request.Text("operation") != "status") throw new InvalidDataException("此平台暂不支持快捷登录");
                    result = new { supported = false }; break;
                case "notificationStatus":
                case "requestNotificationPermission":
                    // Balloon sounds follow Windows' own notification settings.
                    result = new { supported = true, permission = "authorized", canOpenSettings = true, sound = false }; break;
                case "showNotification":
                {
                    var notifTitle = request.Text("title");
                    var notifBody = request.Text("body");
                    var terminalID = request.Text("terminal_id");
                    var connectionID = request.Text("connection_id");
                    if (notifTitle.Length is 0 or > 200) throw new InvalidDataException("通知标题无效");
                    if (notifBody.Length > 500) throw new InvalidDataException("通知内容过长");
                    if (terminalID.Length > 0 && !System.Text.RegularExpressions.Regex.IsMatch(terminalID, "^[a-f0-9]{16}$"))
                        throw new InvalidDataException("通知目标终端无效");
                    if (connectionID.Length > 128) throw new InvalidDataException("通知连接标识无效");
                    if (pendingNotification != null && pendingNotification.Value != (terminalID, connectionID))
                        pendingNotificationAmbiguous = true;
                    pendingNotification = (terminalID, connectionID);
                    tray.ShowBalloonTip(5000, notifTitle, notifBody.Length > 0 ? notifBody : notifTitle, ToolTipIcon.None);
                    result = new { delivered = true, permission = "authorized" };
                    break;
                }
                case "openNotificationSettings":
                    System.Diagnostics.Process.Start(new System.Diagnostics.ProcessStartInfo("ms-settings:notifications") { UseShellExecute = true });
                    result = true; break;
                case "refreshSession":
                    var (exit, data) = await Backend.Json(controllerNeedsUpdate ? ["desktop", "--json", "--existing"] : ["desktop", "--json"], lifetime.Token);
                    if (exit != 0) throw new IOException("工作台会话恢复失败");
                    result = new { url = UpdatePolicy.Entry(data.Text("url")).AbsoluteUri }; break;
                case "reconnect": result = true; _ = Bootstrap(); break;
                case "quitNow": result = true; allowQuit = true; BeginInvoke(Close); break;
                default: throw new InvalidDataException("未知原生操作");
            }
            if (!web.IsDisposed) web.CoreWebView2.PostWebMessageAsJson(JsonSerializer.Serialize(new { id, result }));
        }
        catch (Exception error)
        {
            _ = Backend.Report(error.ToString());
            if (id.Length != 0 && !IsDisposed && !web.IsDisposed) web.CoreWebView2.PostWebMessageAsJson(JsonSerializer.Serialize(new { id, error = error.Message }));
        }
        finally { if (counted) nativeRequests--; }
    }

    private const string SmokeDiagnosticsScript = """
        (() => {
          if (window !== window.top || location.protocol !== 'http:' || location.hostname !== 'localhost') return;
          const errors = [];
          Object.defineProperty(window, '__cageSmokeErrors', { value: errors });
          const ownPath = value => {
            try { const url = new URL(value, location.href); return url.origin === location.origin ? url.pathname : ''; }
            catch { return ''; }
          };
          const scrub = value => {
            let text = String(value || '');
            for (const input of document.querySelectorAll('input,textarea'))
              if (input.value) text = text.split(input.value).join('[form-value]');
            return text.replace(/https?:\/\/[^\s"'<>`]+/g, value => ownPath(value) || '[external]')
              .replace(/[?#][^\s"'<>)]*/g, '[redacted]').slice(0, 768);
          };
          const stack = error => String(error?.stack || '').split('\n').slice(1)
            .filter(line => line.includes(location.origin + '/')).slice(0, 6).map(scrub);
          const record = value => { if (errors.length < 8) errors.push(value); };
          addEventListener('error', event => {
            if (event.target instanceof HTMLScriptElement || event.target instanceof HTMLLinkElement) {
              const source = ownPath(event.target.src || event.target.href);
              if (source) record({ type: 'module-load', source });
              return;
            }
            const source = ownPath(event.filename || location.href);
            if (source) record({ type: 'error', source, message: scrub(event.message), stack: stack(event.error) });
          }, true);
          addEventListener('unhandledrejection', event => {
            const frames = stack(event.reason);
            // Primitive/cross-origin rejections are counted without exporting arbitrary values.
            record({ type: 'unhandledrejection', message: frames.length ? scrub(event.reason?.message) : 'Unhandled rejection', stack: frames });
          });
        })();
        """;

    private const string BridgeScript = """
        (() => {
          if (window !== window.top || location.protocol !== 'http:' || location.hostname !== 'localhost') return;
          window.cageNativeIMEAvailable = true;
          const pending = new Map();
          const fileActions = new Set([
            'uploadHostFiles', 'chooseUploadFiles', 'clipboardSyncRead', 'clipboardSyncWrite',
            'saveBrowserDownload', 'browserDownloadAction', 'desktopFileAction', 'exportDiagnostics',
            'importTheme', 'exportTheme', 'saveRecoveryKey', 'shareLocalProject', 'chooseFolder', 'chooseGuestMedia',
          ]);
          chrome.webview.addEventListener('message', ({data}) => {
            const item = pending.get(data.id); if (!item) return;
            pending.delete(data.id); clearTimeout(item.timer);
            data.error ? item.reject(new Error(data.error)) : item.resolve(data.result);
          });
          const cage = {postMessage(request) { return new Promise((resolve, reject) => {
            const id = crypto.randomUUID();
            // File commands have a 30-minute backend deadline. Leave time for
            // the native file dialog and the final subprocess response.
            const timer = setTimeout(() => { pending.delete(id); reject(new Error('原生操作超时，请重试')); }, fileActions.has(request.action) ? 3600000 : 240000);
            pending.set(id, {resolve, reject, timer}); chrome.webview.postMessage({id, request});
          }); }};
          Object.defineProperty(window, 'webkit', {value: {messageHandlers: {cage}}, writable: false});
        })();
        """;
}

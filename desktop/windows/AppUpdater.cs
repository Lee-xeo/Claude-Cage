using System.Diagnostics;
using System.Globalization;
using System.Reflection;
using System.Security.Cryptography;
using System.Text.Json;

namespace Cage.Windows;

internal sealed class AppUpdater : IDisposable
{
    public static string Version => typeof(AppUpdater).Assembly.GetCustomAttribute<AssemblyInformationalVersionAttribute>()?.InformationalVersion ?? "development";
    public static string Root => Path.Combine(Backend.Root, "updates");
    private readonly Action<Dictionary<string, object?>> changed;
    private readonly Action quit, reconnect;
    private readonly Func<string> readPublisher;
    private readonly Func<bool> canUpgradeUnsigned;
    private readonly Action<string>? report;
    private readonly Dictionary<string, object?> state = new() { ["phase"] = "idle", ["currentVersion"] = Version, ["message"] = "" };
    private CancellationTokenSource? cancellation;
    private InstallPlan? prepared;
    private bool committed;

    public AppUpdater(Action<Dictionary<string, object?>> changed, Action quit, Action reconnect, Func<string>? readPublisher = null, Action<string>? report = null, Func<bool>? canUpgradeUnsigned = null)
    {
        this.changed = changed; this.quit = quit; this.reconnect = reconnect;
        this.readPublisher = readPublisher ?? Authenticode.OwnPublisher;
        this.canUpgradeUnsigned = canUpgradeUnsigned ?? DefaultCanUpgradeUnsigned;
        this.report = report;
        try
        {
            var result = Path.Combine(Root, "last-result.json");
            if (File.Exists(result) && new FileInfo(result).Length <= 16384)
            {
                var value = JsonDocument.Parse(File.ReadAllText(result)).RootElement;
                state["message"] = value.Text("message");
                if (value.TryGetProperty("success", out var success) && !success.GetBoolean()) { state["phase"] = "failed"; state["retryable"] = true; }
                try { File.Delete(result); } catch (IOException) { } catch (UnauthorizedAccessException) { }
            }
        }
        catch (Exception e) when (e is IOException or JsonException or UnauthorizedAccessException) { }
    }

    public Dictionary<string, object?> Handle(string operation)
    {
        if (Backend.ClientOnly)
        {
            if (state["phase"] as string != "manual" || operation != "status")
                Change("manual", "轻量客户端使用独立安装包更新；连接配置会保留，云端任务不受影响。");
            return new(state);
        }
        if (operation is "check" or "install" &&
            DateTimeOffset.TryParse(state.GetValueOrDefault("retryAt") as string, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var retryAt) && retryAt > DateTimeOffset.UtcNow)
            return new(state);
        switch (operation)
        {
            case "status": break;
            case "check": if (cancellation == null && prepared == null) _ = Run(true); break;
            case "install":
                if (cancellation == null)
                {
                    if (prepared != null) { state["officialUpgrade"] = prepared.UnsignedSource; Change("ready", "更新已下载，请保存工作并手动停止环境后安装", true); }
                    else _ = Run(false);
                }
                break;
            case "complete": if (cancellation == null && prepared != null) _ = Install(); break;
            case "cancel": if (!committed && state["phase"] as string != "installing") cancellation?.Cancel(); break;
            default: throw new InvalidDataException("更新操作无效");
        }
        return new(state);
    }

    private static bool DefaultCanUpgradeUnsigned()
    {
        if (Environment.GetEnvironmentVariable("CAGE_DISABLE_OFFICIAL_UPGRADE") == "1") return false;
        try
        {
            var target = AppContext.BaseDirectory.TrimEnd(Path.DirectorySeparatorChar);
            if (string.IsNullOrEmpty(target)) return false;
            UpdatePolicy.PlainPath(target);
            var idPath = Path.Combine(target, "Cage.install-id");
            if (!File.Exists(idPath)) return false;
            UpdatePolicy.PlainPath(idPath);
            return File.ReadAllText(idPath).Trim() == "io.github.lee-xeo.claude-cage";
        }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException or InvalidDataException)
        {
            return false;
        }
    }

    private void Change(string phase, string message, bool retryable = false, string retryAt = "")
    {
        if (phase is "current" or "manual") state["officialUpgrade"] = false;
        state["phase"] = phase; state["message"] = message; state["retryable"] = retryable; state["retryAt"] = retryAt; changed(new(state));
    }

    public void MarkReady()
    {
        if (!Guid.TryParse(Environment.GetEnvironmentVariable("CAGE_UPDATE_CONFIRMATION"), out var token)) return;
        Directory.CreateDirectory(Root); UpdatePolicy.PlainPath(Root);
        File.WriteAllText(Path.Combine(Root, "launch-" + token + ".json"), JsonSerializer.Serialize(new { version = Version }));
        Change("current", "Cage 已更新至 " + Version);
        Environment.SetEnvironmentVariable("CAGE_UPDATE_CONFIRMATION", null);
    }

    private async Task Run(bool checkOnly)
    {
        // The backend reports progress at least once per retry. Bound its silence
        // rather than the transfer, so a slow connection can still finish.
        var silence = TimeSpan.FromMinutes(10);
        cancellation = new CancellationTokenSource(silence);
        var token = cancellation.Token;
        Change("checking", "正在检查更新，优先使用主机网络");
        string? archive = null;
        string retryAt = "";
        using var process = new Process();
        var started = false;
        try
        {
            // Signed installs pin strictly to their own certificate. Eligible
            // unsigned source builds installed via the Cage installer may
            // upgrade in-place to the pinned official signed release; all other
            // unsigned builds fall back to manual download without auto-replacing.
            string? publisher = null;
            var unsignedSource = false;
            try { publisher = await Task.Run(readPublisher, token); }
            catch (Exception error) when (error is IOException or CryptographicException or UnauthorizedAccessException)
            {
                report?.Invoke("Windows publisher check failed: " + error.Message);
                if (await Task.Run(canUpgradeUnsigned, token))
                {
                    publisher = Authenticode.OfficialPublisherKey;
                    unsignedSource = true;
                }
            }
            token.ThrowIfCancellationRequested();
            process.StartInfo = Backend.StartInfo(Backend.Executable, "app-update", checkOnly || publisher == null ? "check" : "download");
            if (!process.Start()) throw new IOException("无法启动更新检查");
            started = true;
            using var abort = token.Register(() => { try { if (!process.HasExited) process.Kill(); } catch (InvalidOperationException) { } });
            var errors = Backend.Drain(process.StandardError, token);
            JsonElement last = default;
            while (await process.StandardOutput.ReadLineAsync(token) is { } line)
            {
                if (line.Length > 65536) throw new IOException("更新响应超过允许大小");
                cancellation.CancelAfter(silence);
                last = JsonDocument.Parse(line).RootElement.Clone();
                retryAt = last.Text("retryAt");
                foreach (var property in last.EnumerateObject())
                    if (property.Name is not ("archivePath" or "sha256")) state[property.Name] = property.Value.ValueKind switch {
                        JsonValueKind.String => property.Value.GetString(),
                        JsonValueKind.True => true, JsonValueKind.False => false,
                        JsonValueKind.Number => property.Value.GetInt64(),
                        _ => property.Value.Clone(),
                    };
                if (publisher == null && state.GetValueOrDefault("phase") as string == "available")
                    Change("manual", "当前 Windows 程序未签名或签名无法验证，请从官方发布页下载安装包，退出 Cage 后安装到原位置；配置、凭据、项目和虚拟机磁盘会保留。");
                else
                {
                    state["officialUpgrade"] = state.GetValueOrDefault("phase") as string != "current" && unsignedSource;
                    changed(new(state));
                }
            }
            await process.WaitForExitAsync(token); await errors;
            if (process.ExitCode != 0) throw new IOException(last.ValueKind == JsonValueKind.Object && last.Text("message") is { Length: > 0 } error ? error : "更新请求未完成");
            if (checkOnly || publisher == null || last.Text("phase") != "downloaded") return;
            archive = UpdatePolicy.DownloadPath(last.Text("archivePath"), Root);
            var version = last.Text("latestVersion"); var digest = last.Text("sha256");
            if (!UpdatePolicy.IsNewer(version, Version)) throw new IOException("安装包版本无效或不高于当前版本");
            cancellation.CancelAfter(TimeSpan.FromMinutes(25));
            Change("verifying", "正在验证 Windows 签名和安装包内容");
            await Task.Run(() => UpdateInstaller.VerifyArchive(archive, digest, version, publisher), token);
            token.ThrowIfCancellationRequested();
            prepared = new InstallPlan(archive, digest, version, publisher,
                AppContext.BaseDirectory.TrimEnd(Path.DirectorySeparatorChar), Environment.ProcessId,
                Process.GetCurrentProcess().StartTime.ToUniversalTime().Ticks, Guid.NewGuid().ToString(), unsignedSource);
            archive = null;
        }
        catch (OperationCanceledException) { Change("failed", "更新已暂停，可稍后重试", true); }
        catch (Exception error) { Change("failed", error.Message, true, retryAt); }
        finally
        {
            if (started && !process.HasExited) process.Kill();
            if (archive != null) { try { Directory.Delete(Path.GetDirectoryName(archive)!, true); } catch (IOException) { } }
            cancellation.Dispose(); cancellation = null;
        }
        if (prepared != null) { state["officialUpgrade"] = prepared.UnsignedSource; Change("ready", "更新已下载，请保存工作并手动停止环境后安装", true); }
    }

    private async Task Install()
    {
        if (prepared == null) return;
        cancellation = new CancellationTokenSource(TimeSpan.FromMinutes(5));
        var token = cancellation.Token;
        Process? helper = null;
        var helperPath = Path.Combine(Root, "helper-" + Guid.NewGuid() + ".exe");
        state["officialUpgrade"] = prepared.UnsignedSource;
        Change("installing", "正在准备安装更新");
        try
        {
            // Validate the destination before stopping a running environment.
            UpdatePolicy.PlainPath(prepared.Target);
            if (prepared.UnsignedSource && !string.Equals(prepared.Publisher, Authenticode.OfficialPublisherKey, StringComparison.Ordinal))
                throw new IOException("安装计划发布者不匹配");
            Directory.CreateDirectory(Root); UpdatePolicy.PlainPath(Root);
            File.Copy(Environment.ProcessPath!, helperPath);
            if (!prepared.UnsignedSource)
                await Task.Run(() => Authenticode.SamePublisher(helperPath, prepared.Publisher), token);
            var start = Backend.StartInfo(helperPath, "--install-update");
            // A live process's working directory cannot be renamed on Windows.
            // The helper outlives this App and must not retain its install path.
            start.WorkingDirectory = Root;
            start.RedirectStandardInput = true;
            helper = new Process { StartInfo = start };
            if (!helper.Start()) throw new IOException("无法启动安全安装程序");
            await helper.StandardInput.WriteLineAsync(JsonSerializer.Serialize(prepared));
            await helper.StandardInput.FlushAsync(token);
            var ready = await helper.StandardOutput.ReadLineAsync(token);
            if (ready != "READY") throw new IOException("安装程序准备失败，原应用已保留");
            var (exit, data) = await Backend.Json(["prepare-update", "--json"], token);
            if (data.Text("status") == "stop_required")
            {
                throw new IOException("环境仍在运行，请在工作台停止环境后重试安装");
            }
            if (exit != 0 || data.Text("status") != "prepared") throw new IOException(data.Text("message") is { Length: > 0 } detail ? detail : "环境未能安全停止");
            if (helper.HasExited) throw new IOException("安装程序已退出，原应用已保留");
            await helper.StandardInput.WriteLineAsync("COMMIT"); await helper.StandardInput.FlushAsync(token);
            helper.StandardInput.Close(); committed = true;
            Change("installing", "正在安装并重启 Cage"); quit();
        }
        catch (Exception error) { Change("failed", error is OperationCanceledException ? "安装准备超时，原应用已保留" : error.Message, true); reconnect(); }
        finally
        {
            if (!committed && helper != null)
            {
                helper.StandardInput.Close();
                try { await helper.WaitForExitAsync().WaitAsync(TimeSpan.FromSeconds(5)); }
                catch (TimeoutException) { if (!helper.HasExited) helper.Kill(); }
            }
            helper?.Dispose();
            if (!committed) { try { File.Delete(helperPath); } catch (IOException) { } }
            cancellation?.Dispose(); cancellation = null;
        }
    }

    public void Dispose() { if (!committed) cancellation?.Cancel(); }
}

internal sealed record InstallPlan(string Archive, string Digest, string Version, string Publisher,
    string Target, int ParentPid, long ParentStartTicks, string Confirmation, bool UnsignedSource = false);

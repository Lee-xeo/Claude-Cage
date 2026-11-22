using System.ComponentModel;
using System.Drawing;
using System.Reflection;
using System.Runtime.ExceptionServices;
using Cage.Windows;

internal static class TrayMenuTests
{
    internal static void Run()
    {
        Exception? failure = null;
        var thread = new Thread(() =>
        {
            try { RunOnUiThread(); }
            catch (Exception error) { failure = error; }
        })
        { IsBackground = true, Name = "Cage tray menu regression" };
        thread.SetApartmentState(ApartmentState.STA);
        thread.Start();
        if (!thread.Join(TimeSpan.FromSeconds(15)))
            throw new TimeoutException("Tray menu regression did not complete");
        if (failure != null) ExceptionDispatchInfo.Capture(failure).Throw();
    }

    private static void RunOnUiThread()
    {
        var assertions = 0;
        void Check(bool value, string message)
        {
            assertions++;
            if (!value) throw new InvalidOperationException(message);
        }

        // 1. Appearance token mapping & contrast guards
        var vscodeDark = WindowChrome.FromAppearance(
            ColorTranslator.FromHtml("#181818"),
            ColorTranslator.FromHtml("#cccccc"),
            dark: true,
            menuBackground: "#181818",
            menuForeground: "#cccccc",
            muted: "#9d9d9d",
            border: "#3c3c3c",
            hover: "#2b2b2b",
            selected: "#353535",
            danger: "#f48771");
        Check(vscodeDark.Palette.MenuBackground == ColorTranslator.FromHtml("#181818"), "exact menu background not preserved");
        Check(vscodeDark.Palette.MenuForeground == ColorTranslator.FromHtml("#cccccc"), "exact menu foreground not preserved");
        Check(vscodeDark.Palette.IconMuted == ColorTranslator.FromHtml("#9d9d9d"), "exact muted icon color not preserved");
        Check(vscodeDark.Palette.Border == ColorTranslator.FromHtml("#3c3c3c"), "exact border color not preserved");
        Check(vscodeDark.Palette.Hover == ColorTranslator.FromHtml("#2b2b2b"), "distinct hover color not preserved");
        Check(vscodeDark.Palette.Pressed == ColorTranslator.FromHtml("#353535"), "distinct pressed color not preserved");
        Check(vscodeDark.Palette.Danger == ColorTranslator.FromHtml("#f48771"), "exact danger color not preserved");

        var codexDark = WindowChrome.FromAppearance(
            ColorTranslator.FromHtml("#252525"),
            ColorTranslator.FromHtml("#ffffff"),
            dark: true,
            menuBackground: "#252525",
            menuForeground: "#ffffff",
            muted: "#dedede",
            border: "#3f3f3f",
            hover: "#2d2d2d",
            selected: "#363636",
            danger: "#f26d78");
        Check(WindowChrome.ChannelDistance(codexDark.Palette.Hover, codexDark.Palette.MenuBackground) >= 16,
            "Codex dark hover lacked visible contrast against menu chrome");
        Check(WindowChrome.ChannelDistance(codexDark.Palette.Pressed, codexDark.Palette.MenuBackground) >
            WindowChrome.ChannelDistance(codexDark.Palette.Hover, codexDark.Palette.MenuBackground),
            "pressed fill was not more pronounced than hover fill");
        Check(WindowChrome.ChannelDistance(codexDark.Palette.IconMuted, codexDark.Palette.MenuForeground) >= 30,
            "muted icon color collapsed onto menu foreground");
        Check(codexDark.Palette.DangerHover.R > codexDark.Palette.MenuBackground.R,
            "destructive hover did not pick up danger tint");

        var fallback = WindowChrome.FromAppearance(
            ColorTranslator.FromHtml("#f4f4f4"),
            ColorTranslator.FromHtml("#1a1c1f"),
            dark: false,
            menuBackground: "bad-hex",
            border: "invalid");
        Check(fallback.Palette.MenuBackground == ColorTranslator.FromHtml("#f4f4f4"), "invalid menuBackground did not fall back");
        Check(WindowChrome.ChannelDistance(fallback.Palette.Border, fallback.Palette.MenuBackground) >= 14,
            "fallback border did not contrast with light menu background");

        var system = WindowChrome.SystemDefault();
        Check(WindowChrome.ChannelDistance(system.Foreground, system.Background) > 100,
            "system default chrome foreground/background lacked contrast");

        // 2. LTR & RTL item geometry
        var (ltrIcon, ltrText) = TrayMenu.ComputeItemRects(new Size(220, 36), 1.25f, rightToLeft: false);
        Check(ltrIcon.Left > 0 && ltrIcon.Right < ltrText.Left, "LTR icon and text rectangles overlapped");
        Check(ltrText.Right < 220, "LTR text rectangle had no right padding");
        Check(Math.Abs((ltrIcon.Top + ltrIcon.Bottom) / 2f - 18f) < 0.5f, "LTR icon was not vertically centered");

        var (rtlIcon, rtlText) = TrayMenu.ComputeItemRects(new Size(220, 36), 1.25f, rightToLeft: true);
        Check(rtlText.Left > 0 && rtlText.Right < rtlIcon.Left && rtlIcon.Right < 220,
            "RTL icon and text rectangles did not mirror cleanly");

        // 3. Layout sizing, shrink-back, handle creation, and offscreen rendering
        using var menu = new TrayMenu();
        var open = new ToolStripMenuItem("打开 Cage") { Tag = new TrayItemMetadata("打开 Cage", TrayIconKind.Open) };
        var update = new ToolStripMenuItem("检查应用更新…") { Tag = new TrayItemMetadata("检查应用更新…", TrayIconKind.CheckUpdates) };
        var about = new ToolStripMenuItem("关于 Cage") { Tag = new TrayItemMetadata("关于 Cage", TrayIconKind.About) };
        var sep = new ToolStripSeparator();
        var quit = new ToolStripMenuItem("退出 Cage…") { Tag = new TrayItemMetadata("退出 Cage…", TrayIconKind.Quit, Destructive: true) };
        menu.Items.AddRange([open, update, about, sep, quit]);

        _ = menu.Handle;
        Check(menu.IsHandleCreated && !menu.Visible, "menu handle fixture was not created offscreen");

        codexDark.ApplyMenu(menu);
        var onOpening = typeof(TrayMenu).GetMethod("OnOpening", BindingFlags.Instance | BindingFlags.NonPublic)!;
        onOpening.Invoke(menu, [new CancelEventArgs()]);

        int narrowWidth = menu.Width;
        int outerPad = menu.LogicalToDeviceUnits(TrayMenu.OuterPadDip);
        Check(menu.AutoSize, "tray menu must stay auto-sized so WinForms never enables scroll buttons");
        Check(narrowWidth >= menu.LogicalToDeviceUnits(214), "menu narrower than minimum width");
        Check(open.Height >= menu.LogicalToDeviceUnits(34), "menu item height below minimum hit target");
        Check(sep.Height == menu.LogicalToDeviceUnits(11), "separator height mismatched");
        Check(open.Bounds.Right >= narrowWidth - outerPad, "item bounds would clip right side of hover pill");
        Check(open.Bounds.Top >= outerPad, "top outer margin was collapsed");
        Check(menu.Height - quit.Bounds.Bottom >= outerPad, "bottom outer margin was collapsed");
        if (OperatingSystem.IsWindowsVersionAtLeast(10, 0, 22000))
            Check(menu.RoundedWindowFrame, "Windows 11 DWM rounded corner frame was not applied");

        open.Text = "Открыть изолированную рабочую область Cage с длинным заголовком";
        onOpening.Invoke(menu, [new CancelEventArgs()]);
        int wideWidth = menu.Width;
        Check(wideWidth > narrowWidth, "menu did not widen for longer localized label");

        open.Text = "打开 Cage";
        onOpening.Invoke(menu, [new CancelEventArgs()]);
        Check(menu.Width == narrowWidth, "menu did not shrink back after switching to shorter label");

        // 4. Offscreen rendering exercises background, pill hover, destructive hover, icons, text, and separator
        using var bitmap = new Bitmap(menu.Width, menu.Height);
        using (var graphics = Graphics.FromImage(bitmap))
        {
            var renderer = menu.Renderer;
            renderer.DrawToolStripBackground(new ToolStripRenderEventArgs(graphics, menu, new Rectangle(Point.Empty, menu.Size), codexDark.Palette.MenuBackground));

            open.Select();
            Check(open.Selected, "menu item could not enter selected state for render check");

            foreach (ToolStripItem item in menu.Items)
            {
                graphics.TranslateTransform(item.Bounds.X, item.Bounds.Y);
                try
                {
                    if (item is ToolStripSeparator separator)
                    {
                        renderer.DrawSeparator(new ToolStripSeparatorRenderEventArgs(graphics, separator, vertical: false));
                    }
                    else
                    {
                        renderer.DrawMenuItemBackground(new ToolStripItemRenderEventArgs(graphics, item));
                        renderer.DrawItemText(new ToolStripItemTextRenderEventArgs(
                            graphics, item, item.Text, new Rectangle(Point.Empty, item.Size),
                            item.ForeColor, item.Font, ContentAlignment.MiddleLeft));
                    }
                }
                finally
                {
                    graphics.ResetTransform();
                }
            }

            // Exercise destructive hover tint directly on quit item bounds as well
            quit.Select();
            graphics.TranslateTransform(quit.Bounds.X, quit.Bounds.Y);
            try
            {
                renderer.DrawMenuItemBackground(new ToolStripItemRenderEventArgs(graphics, quit));
                renderer.DrawItemText(new ToolStripItemTextRenderEventArgs(
                    graphics, quit, quit.Text, new Rectangle(Point.Empty, quit.Size),
                    quit.ForeColor, quit.Font, ContentAlignment.MiddleLeft));
            }
            finally
            {
                graphics.ResetTransform();
            }

            renderer.DrawToolStripBorder(new ToolStripRenderEventArgs(graphics, menu, new Rectangle(Point.Empty, menu.Size), codexDark.Palette.MenuBackground));
        }

        // Verify symmetric outer margins, hover fill on both ends, no top scroll arrow, and destructive danger tint
        var bg = codexDark.Palette.MenuBackground;
        int openMidY = open.Bounds.Y + open.Bounds.Height / 2;
        int quitMidY = quit.Bounds.Y + quit.Bounds.Height / 2;
        var topCenter = bitmap.GetPixel(menu.Width / 2, 1);
        Check(WindowChrome.ChannelDistance(topCenter, bg) == 0, "unexpected top margin artifact or scroll button rendered");
        var leftEdge = bitmap.GetPixel(1, openMidY);
        var rightEdge = bitmap.GetPixel(menu.Width - 2, openMidY);
        Check(WindowChrome.ChannelDistance(leftEdge, bg) == 0, "left outer margin was painted over by hover pill");
        Check(WindowChrome.ChannelDistance(rightEdge, bg) == 0, "right outer margin was painted over by hover pill");
        var openLeft = bitmap.GetPixel(outerPad + 4, openMidY);
        var openRight = bitmap.GetPixel(menu.Width - 1 - (outerPad + 4), openMidY);
        Check(WindowChrome.ChannelDistance(openLeft, bg) >= 10, "left side of selected menu item pill was not painted");
        Check(WindowChrome.ChannelDistance(openRight, bg) >= 10, "right side of selected menu item pill was not painted");
        var quitLeft = bitmap.GetPixel(outerPad + 4, quitMidY);
        var quitRight = bitmap.GetPixel(menu.Width - 1 - (outerPad + 4), quitMidY);
        Check(quitLeft.R > quitLeft.G + 10 && quitRight.R > quitRight.G + 10,
            "destructive hover pill did not render danger tint symmetrically");

        Console.WriteLine($"Windows tray menu: {assertions} assertions passed; palette, contrast, layout shrink, symmetric margins, RTL/LTR rects, and rendering verified.");
    }
}

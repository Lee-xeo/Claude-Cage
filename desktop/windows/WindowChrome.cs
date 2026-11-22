using System.Drawing.Drawing2D;
using System.Runtime.InteropServices;
using System.Text.RegularExpressions;
using Microsoft.Win32;

namespace Cage.Windows;

internal sealed record MenuPalette(
    Color Background,
    Color Foreground,
    bool Dark,
    Color MenuBackground,
    Color MenuForeground,
    Color IconMuted,
    Color Border,
    Color Separator,
    Color Hover,
    Color Pressed,
    Color Danger,
    Color DangerHover,
    Color DangerPressed);

internal sealed partial class WindowChrome
{
    private const int DwmUseImmersiveDarkMode = 20;
    private const int DwmWindowCornerPreference = 33;
    private const int DwmBorderColor = 34;
    private const int DwmCaptionColor = 35;
    private const int DwmTextColor = 36;
    private const int DwmCornerRound = 2;
    private const int DwmColorDefault = -1;

    private readonly MenuPalette palette;

    [DllImport("dwmapi.dll")]
    private static extern int DwmSetWindowAttribute(IntPtr hwnd, int attribute, ref int value, int size);

    [GeneratedRegex("^#[0-9a-fA-F]{6}$")]
    private static partial Regex HexColorPattern();

    internal WindowChrome(Color background, Color foreground, bool dark)
        : this(CreatePalette(background, foreground, dark)) { }

    private WindowChrome(MenuPalette palette) => this.palette = palette;

    internal Color Background => palette.Background;
    internal Color Foreground => palette.Foreground;
    internal bool Dark => palette.Dark;
    internal MenuPalette Palette => palette;

    internal static Color ContrastingText(Color value) =>
        value.GetBrightness() < 0.5 ? Color.White : Color.Black;

    internal static bool TryParseHexColor(string? value, out Color color)
    {
        if (!string.IsNullOrEmpty(value) && HexColorPattern().IsMatch(value))
        {
            color = ColorTranslator.FromHtml(value);
            return true;
        }
        color = default;
        return false;
    }

    internal static Color Mix(Color a, Color b, double weight)
    {
        weight = Math.Clamp(weight, 0, 1);
        int r = (int)Math.Round(a.R + (b.R - a.R) * weight);
        int g = (int)Math.Round(a.G + (b.G - a.G) * weight);
        int bl = (int)Math.Round(a.B + (b.B - a.B) * weight);
        return Color.FromArgb(Math.Clamp(r, 0, 255), Math.Clamp(g, 0, 255), Math.Clamp(bl, 0, 255));
    }

    internal static int ChannelDistance(Color a, Color b) =>
        Math.Max(Math.Abs(a.R - b.R), Math.Max(Math.Abs(a.G - b.G), Math.Abs(a.B - b.B)));

    internal static Color EnsureContrast(Color candidate, Color surface, Color ink, int minDistance, double fallbackMix) =>
        ChannelDistance(candidate, surface) >= minDistance ? candidate : Mix(surface, ink, fallbackMix);

    internal static bool IsSystemDarkTheme()
    {
        try
        {
            using var key = Registry.CurrentUser.OpenSubKey(@"Software\Microsoft\Windows\CurrentVersion\Themes\Personalize");
            return key?.GetValue("AppsUseLightTheme") is int value && value == 0;
        }
        catch (Exception error) when (error is System.Security.SecurityException or UnauthorizedAccessException or IOException)
        {
            return false;
        }
    }

    internal static WindowChrome SystemDefault() => IsSystemDarkTheme()
        ? FromAppearance(
            ColorTranslator.FromHtml("#252525"),
            Color.White,
            dark: true,
            menuBackground: "#252525",
            menuForeground: "#ffffff",
            border: "#3f3f3f",
            danger: "#f26d78")
        : FromAppearance(
            ColorTranslator.FromHtml("#f4f4f4"),
            ColorTranslator.FromHtml("#1a1c1f"),
            dark: false,
            menuBackground: "#f4f4f4",
            menuForeground: "#1a1c1f",
            border: "#dddede",
            danger: "#bb3535");

    internal static WindowChrome FromAppearance(
        Color background,
        Color foreground,
        bool dark,
        string? menuBackground = null,
        string? menuForeground = null,
        string? muted = null,
        string? border = null,
        string? hover = null,
        string? selected = null,
        string? danger = null) =>
        new(CreatePalette(background, foreground, dark, menuBackground, menuForeground, muted, border, hover, selected, danger));

    private static MenuPalette CreatePalette(
        Color background,
        Color foreground,
        bool dark,
        string? menuBackground = null,
        string? menuForeground = null,
        string? muted = null,
        string? border = null,
        string? hover = null,
        string? selected = null,
        string? danger = null)
    {
        var menuBg = TryParseHexColor(menuBackground, out var parsedMenuBg) ? parsedMenuBg : background;
        var menuFg = TryParseHexColor(menuForeground, out var parsedMenuFg) ? parsedMenuFg : foreground;

        var iconMuted = TryParseHexColor(muted, out var parsedMuted) && ChannelDistance(parsedMuted, menuFg) >= 30
            ? parsedMuted
            : Mix(menuFg, menuBg, 0.35);

        var borderColor = TryParseHexColor(border, out var parsedBorder)
            ? EnsureContrast(parsedBorder, menuBg, menuFg, 14, dark ? 0.12 : 0.10)
            : Mix(menuBg, menuFg, dark ? 0.12 : 0.10);

        var separatorColor = Mix(menuBg, borderColor, 0.80);

        double hoverFallbackMix = dark ? 0.085 : 0.075;
        var hoverColor = TryParseHexColor(hover, out var parsedHover)
            ? EnsureContrast(parsedHover, menuBg, menuFg, 16, hoverFallbackMix)
            : Mix(menuBg, menuFg, hoverFallbackMix);

        double pressedFallbackMix = dark ? 0.13 : 0.11;
        var pressedColor = TryParseHexColor(selected, out var parsedSelected)
            ? EnsureContrast(parsedSelected, menuBg, menuFg, 22, pressedFallbackMix)
            : Mix(menuBg, menuFg, pressedFallbackMix);
        if (ChannelDistance(pressedColor, menuBg) <= ChannelDistance(hoverColor, menuBg) + 2)
            pressedColor = Mix(hoverColor, menuFg, 0.06);

        var dangerColor = TryParseHexColor(danger, out var parsedDanger)
            ? parsedDanger
            : ColorTranslator.FromHtml(dark ? "#f26d78" : "#bb3535");
        var dangerHover = Mix(menuBg, dangerColor, dark ? 0.16 : 0.10);
        var dangerPressed = Mix(menuBg, dangerColor, dark ? 0.24 : 0.16);

        return new MenuPalette(
            background,
            foreground,
            dark,
            menuBg,
            menuFg,
            iconMuted,
            borderColor,
            separatorColor,
            hoverColor,
            pressedColor,
            dangerColor,
            dangerHover,
            dangerPressed);
    }

    internal void Apply(IntPtr window, MenuStrip? menus)
    {
        bool accessible = SystemInformation.HighContrast;
        int darkFrame = palette.Dark && !accessible ? 1 : 0;
        _ = DwmSetWindowAttribute(window, DwmUseImmersiveDarkMode, ref darkFrame, sizeof(int));
        if (OperatingSystem.IsWindowsVersionAtLeast(10, 0, 22000))
        {
            int caption = accessible ? DwmColorDefault : ColorTranslator.ToWin32(palette.Background);
            int text = accessible ? DwmColorDefault : ColorTranslator.ToWin32(palette.Foreground);
            _ = DwmSetWindowAttribute(window, DwmCaptionColor, ref caption, sizeof(int));
            _ = DwmSetWindowAttribute(window, DwmTextColor, ref text, sizeof(int));
        }

        if (menus != null) ApplyMenu(menus);
    }

    internal void ApplyDropDownFrame(IntPtr handle, out bool rounded)
    {
        bool accessible = SystemInformation.HighContrast;
        int darkFrame = palette.Dark && !accessible ? 1 : 0;
        _ = DwmSetWindowAttribute(handle, DwmUseImmersiveDarkMode, ref darkFrame, sizeof(int));

        if (!accessible && OperatingSystem.IsWindowsVersionAtLeast(10, 0, 22000))
        {
            int corner = DwmCornerRound;
            int hrCorner = DwmSetWindowAttribute(handle, DwmWindowCornerPreference, ref corner, sizeof(int));
            int border = ColorTranslator.ToWin32(palette.Border);
            _ = DwmSetWindowAttribute(handle, DwmBorderColor, ref border, sizeof(int));
            rounded = hrCorner == 0;
        }
        else
        {
            if (OperatingSystem.IsWindowsVersionAtLeast(10, 0, 22000))
            {
                int border = DwmColorDefault;
                _ = DwmSetWindowAttribute(handle, DwmBorderColor, ref border, sizeof(int));
            }
            rounded = false;
        }
    }

    internal void ApplyMenu(ToolStrip menu)
    {
        if (menu is TrayMenu trayMenu)
            trayMenu.AttachChrome(this);

        bool accessible = SystemInformation.HighContrast;
        ToolStripRenderer renderer = CreateMenuRenderer(accessible);
        ApplyMenu(menu, renderer, accessible);
    }

    internal ToolStripRenderer CreateMenuRenderer(bool highContrast) =>
        highContrast ? new ToolStripSystemRenderer() : new MenuRenderer(palette);

    private void ApplyMenu(ToolStrip menu, ToolStripRenderer renderer, bool accessible)
    {
        menu.Renderer = renderer;
        menu.BackColor = accessible ? SystemColors.Menu : palette.MenuBackground;
        menu.ForeColor = accessible ? SystemColors.MenuText : palette.MenuForeground;
        foreach (ToolStripItem item in menu.Items)
        {
            item.ForeColor = menu.ForeColor;
            if (item is ToolStripMenuItem { HasDropDownItems: true } child)
                ApplyMenu(child.DropDown, renderer, accessible);
        }
        menu.Invalidate();
    }

    internal static GraphicsPath CreateRoundedRectPath(RectangleF bounds, float radius)
    {
        var path = new GraphicsPath();
        float diameter = Math.Min(radius * 2f, Math.Min(bounds.Width, bounds.Height));
        if (diameter <= 0.1f)
        {
            path.AddRectangle(bounds);
            return path;
        }

        path.AddArc(bounds.X, bounds.Y, diameter, diameter, 180, 90);
        path.AddArc(bounds.Right - diameter, bounds.Y, diameter, diameter, 270, 90);
        path.AddArc(bounds.Right - diameter, bounds.Bottom - diameter, diameter, diameter, 0, 90);
        path.AddArc(bounds.X, bounds.Bottom - diameter, diameter, diameter, 90, 90);
        path.CloseFigure();
        return path;
    }

    private sealed class MenuRenderer(MenuPalette palette) : ToolStripProfessionalRenderer
    {
        private static float DpiScale(ToolStrip? strip, Graphics graphics)
        {
            int dpi = strip is { IsDisposed: false } ? strip.DeviceDpi : 0;
            if (dpi > 0) return Math.Max(1f, dpi / 96f);
            return Math.Max(1f, graphics.DpiX / 96f);
        }

        protected override void OnRenderToolStripBackground(ToolStripRenderEventArgs e)
        {
            using var brush = new SolidBrush(palette.MenuBackground);
            e.Graphics.FillRectangle(brush, e.AffectedBounds);
        }

        protected override void OnRenderImageMargin(ToolStripRenderEventArgs e) { }

        private static void FillRoundedPill(Graphics graphics, Brush brush, RectangleF bounds, float scale)
        {
            float radius = Math.Min(6f * scale, Math.Min(bounds.Width, bounds.Height) / 2f);
            var prevSmoothing = graphics.SmoothingMode;
            var prevOffset = graphics.PixelOffsetMode;
            graphics.SmoothingMode = SmoothingMode.AntiAlias;
            graphics.PixelOffsetMode = PixelOffsetMode.HighQuality;
            try
            {
                using var path = CreateRoundedRectPath(bounds, radius);
                graphics.FillPath(brush, path);
            }
            finally
            {
                graphics.SmoothingMode = prevSmoothing;
                graphics.PixelOffsetMode = prevOffset;
            }
        }

        protected override void OnRenderMenuItemBackground(ToolStripItemRenderEventArgs e)
        {
            if (!e.Item.Enabled || (!e.Item.Selected && !e.Item.Pressed)) return;

            bool destructive = e.Item.Tag is TrayItemMetadata { Destructive: true };
            Color fill = destructive
                ? (e.Item.Pressed ? palette.DangerPressed : palette.DangerHover)
                : (e.Item.Pressed ? palette.Pressed : palette.Hover);

            using var brush = new SolidBrush(fill);
            if (e.ToolStrip is TrayMenu trayMenu)
            {
                float scale = DpiScale(e.ToolStrip, e.Graphics);
                float outerPad = TrayMenu.OuterPadDip * scale;
                float left = outerPad - e.Item.Bounds.X;
                float right = trayMenu.Width - outerPad - e.Item.Bounds.X;
                var bounds = new RectangleF(left, 0.5f, Math.Max(1f, right - left), Math.Max(1f, e.Item.Height - 1f));
                FillRoundedPill(e.Graphics, brush, bounds, scale);
            }
            else if (e.ToolStrip is ToolStripDropDown)
            {
                float scale = DpiScale(e.ToolStrip, e.Graphics);
                var bounds = new RectangleF(0.5f, 0.5f, Math.Max(1f, e.Item.Width - 1f), Math.Max(1f, e.Item.Height - 1f));
                FillRoundedPill(e.Graphics, brush, bounds, scale);
            }
            else
            {
                e.Graphics.FillRectangle(brush, new Rectangle(Point.Empty, e.Item.Size));
            }
        }

        protected override void OnRenderItemText(ToolStripItemTextRenderEventArgs e)
        {
            if (e.ToolStrip is TrayMenu trayMenu)
            {
                float scale = DpiScale(e.ToolStrip, e.Graphics);
                bool rtl = trayMenu.RightToLeft == RightToLeft.Yes;
                var (iconRect, textRect) = TrayMenu.ComputeItemRects(e.Item.Bounds, trayMenu.Width, scale, rtl);

                bool destructive = e.Item.Tag is TrayItemMetadata { Destructive: true };
                bool active = e.Item.Enabled && (e.Item.Selected || e.Item.Pressed);

                Color textColor = !e.Item.Enabled
                    ? palette.IconMuted
                    : destructive && active
                        ? palette.Danger
                        : palette.MenuForeground;

                Color iconColor = !e.Item.Enabled
                    ? palette.Border
                    : destructive && active
                        ? palette.Danger
                        : active
                            ? palette.MenuForeground
                            : palette.IconMuted;

                if (e.Item.Tag is TrayItemMetadata { Icon: not TrayIconKind.None } meta)
                    TrayMenuIcons.Draw(e.Graphics, meta.Icon, iconRect, iconColor, scale);

                var flags = TextFormatFlags.SingleLine
                    | TextFormatFlags.VerticalCenter
                    | TextFormatFlags.EndEllipsis
                    | TextFormatFlags.NoPrefix
                    | TextFormatFlags.NoPadding
                    | (rtl ? TextFormatFlags.Right | TextFormatFlags.RightToLeft : TextFormatFlags.Left);

                TextRenderer.DrawText(e.Graphics, e.Text, e.TextFont, textRect, textColor, flags);
                return;
            }

            e.TextColor = palette.MenuForeground;
            base.OnRenderItemText(e);
        }

        protected override void OnRenderSeparator(ToolStripSeparatorRenderEventArgs e)
        {
            float scale = DpiScale(e.ToolStrip, e.Graphics);
            using var pen = new Pen(palette.Separator, 1f);
            int y = e.Item.Height / 2;
            if (e.ToolStrip is TrayMenu trayMenu)
            {
                int edgeInset = Math.Max(12, (int)Math.Round((TrayMenu.OuterPadDip + 8f) * scale));
                int left = Math.Max(0, edgeInset - e.Item.Bounds.X);
                int right = Math.Min(e.Item.Width - 1, trayMenu.Width - 1 - edgeInset - e.Item.Bounds.X);
                if (right > left)
                    e.Graphics.DrawLine(pen, left, y, right, y);
                return;
            }

            int inset = Math.Max(8, (int)Math.Round(8f * scale));
            if (e.Item.Width <= inset * 2 + 1) return;
            e.Graphics.DrawLine(pen, inset, y, e.Item.Width - inset - 1, y);
        }

        protected override void OnRenderToolStripBorder(ToolStripRenderEventArgs e)
        {
            if (e.ToolStrip is not ToolStripDropDown) return;
            if (e.ToolStrip is TrayMenu { RoundedWindowFrame: true }) return;
            using var pen = new Pen(palette.Border);
            e.Graphics.DrawRectangle(pen, 0, 0, e.ToolStrip.Width - 1, e.ToolStrip.Height - 1);
        }

        protected override void OnRenderArrow(ToolStripArrowRenderEventArgs e)
        {
            e.ArrowColor = palette.MenuForeground;
            base.OnRenderArrow(e);
        }
    }
}

using System.ComponentModel;
using System.Drawing.Drawing2D;

namespace Cage.Windows;

internal enum TrayIconKind
{
    None,
    Open,
    CheckUpdates,
    About,
    Quit,
}

internal sealed record TrayItemMetadata(string SourceKey, TrayIconKind Icon, bool Destructive = false);

internal static class TrayMenuIcons
{
    internal static void Draw(Graphics graphics, TrayIconKind kind, RectangleF bounds, Color color, float scale)
    {
        if (kind == TrayIconKind.None || bounds.Width <= 0 || bounds.Height <= 0) return;

        float stroke = Math.Max(1.25f, 1.35f * scale);
        var prevSmoothing = graphics.SmoothingMode;
        var prevOffset = graphics.PixelOffsetMode;
        graphics.SmoothingMode = SmoothingMode.AntiAlias;
        graphics.PixelOffsetMode = PixelOffsetMode.HighQuality;
        try
        {
            using var pen = new Pen(color, stroke)
            {
                StartCap = LineCap.Round,
                EndCap = LineCap.Round,
                LineJoin = LineJoin.Round,
            };

            PointF Pt(float x, float y) => new(
                bounds.X + x / 16f * bounds.Width,
                bounds.Y + y / 16f * bounds.Height);

            RectangleF Rect(float x, float y, float w, float h) => new(
                bounds.X + x / 16f * bounds.Width,
                bounds.Y + y / 16f * bounds.Height,
                w / 16f * bounds.Width,
                h / 16f * bounds.Height);

            switch (kind)
            {
                case TrayIconKind.Open:
                {
                    using var frame = WindowChrome.CreateRoundedRectPath(
                        Rect(2f, 3f, 12f, 10f),
                        2f / 16f * bounds.Width);
                    graphics.DrawPath(pen, frame);
                    graphics.DrawLine(pen, Pt(2.3f, 6.2f), Pt(13.7f, 6.2f));
                    break;
                }
                case TrayIconKind.CheckUpdates:
                {
                    var arcRect = Rect(2.5f, 2.5f, 11f, 11f);
                    graphics.DrawArc(pen, arcRect, -145f, 115f);
                    graphics.DrawLines(pen, [Pt(12.6f, 2.85f), Pt(12.76f, 5.25f), Pt(10.36f, 5.1f)]);
                    graphics.DrawArc(pen, arcRect, 35f, 115f);
                    graphics.DrawLines(pen, [Pt(3.4f, 13.15f), Pt(3.24f, 10.75f), Pt(5.64f, 10.9f)]);
                    break;
                }
                case TrayIconKind.About:
                {
                    graphics.DrawEllipse(pen, Rect(2.2f, 2.2f, 11.6f, 11.6f));
                    graphics.DrawLine(pen, Pt(8f, 7.6f), Pt(8f, 11.2f));
                    float dotRadius = Math.Max(0.85f, 0.85f / 16f * bounds.Width);
                    var center = Pt(8f, 5f);
                    using var dotBrush = new SolidBrush(color);
                    graphics.FillEllipse(dotBrush,
                        center.X - dotRadius, center.Y - dotRadius,
                        dotRadius * 2f, dotRadius * 2f);
                    break;
                }
                case TrayIconKind.Quit:
                {
                    graphics.DrawLines(pen, [Pt(6.8f, 2.6f), Pt(3.8f, 2.6f), Pt(3.8f, 13.4f), Pt(6.8f, 13.4f)]);
                    graphics.DrawLine(pen, Pt(6.8f, 8f), Pt(13.2f, 8f));
                    graphics.DrawLines(pen, [Pt(10.6f, 5.4f), Pt(13.2f, 8f), Pt(10.6f, 10.6f)]);
                    break;
                }
            }
        }
        finally
        {
            graphics.SmoothingMode = prevSmoothing;
            graphics.PixelOffsetMode = prevOffset;
        }
    }
}

internal sealed class TrayMenu : ContextMenuStrip
{
    internal const int OuterPadDip = 5;
    private const int ItemMarginDip = 1;
    private const int LeftPadDip = 11;
    private const int IconSizeDip = 15;
    private const int IconGapDip = 10;
    private const int RightPadDip = 14;
    private const int MinTotalWidthDip = 214;
    private const int MinItemHeightDip = 34;
    private const int ItemVerticalPadDip = 14;
    private const int SeparatorHeightDip = 11;

    private readonly Font menuFont;
    private WindowChrome? activeChrome;

    internal bool RoundedWindowFrame { get; private set; }

    public TrayMenu()
    {
        ShowImageMargin = false;
        ShowCheckMargin = false;
        DropShadowEnabled = true;
        var baseFont = SystemFonts.MenuFont ?? DefaultFont;
        menuFont = new Font(baseFont.FontFamily, 9.5f, FontStyle.Regular, GraphicsUnit.Point);
        Font = menuFont;
    }

    internal static (RectangleF IconRect, Rectangle TextRect) ComputeItemRects(
        Size itemSize, float scale, bool rightToLeft) =>
        ComputeItemRects(new Rectangle(Point.Empty, itemSize), itemSize.Width, scale, rightToLeft);

    internal static (RectangleF IconRect, Rectangle TextRect) ComputeItemRects(
        Rectangle itemBounds, int menuWidth, float scale, bool rightToLeft)
    {
        float outerPad = OuterPadDip * scale;
        float pillLeft = outerPad - itemBounds.X;
        float pillRight = menuWidth - outerPad - itemBounds.X;
        float leftPad = LeftPadDip * scale;
        float iconSize = IconSizeDip * scale;
        float gap = IconGapDip * scale;
        float rightPad = RightPadDip * scale;
        float iconY = (itemBounds.Height - iconSize) / 2f;

        if (!rightToLeft)
        {
            var iconRect = new RectangleF(pillLeft + leftPad, iconY, iconSize, iconSize);
            int textX = (int)Math.Round(pillLeft + leftPad + iconSize + gap);
            int textRight = Math.Max(textX, (int)Math.Round(pillRight - rightPad));
            var textRect = new Rectangle(textX, 0, Math.Max(0, textRight - textX), itemBounds.Height);
            return (iconRect, textRect);
        }
        else
        {
            float iconX = pillRight - leftPad - iconSize;
            var iconRect = new RectangleF(iconX, iconY, iconSize, iconSize);
            int textLeft = (int)Math.Round(pillLeft + rightPad);
            int textRight = Math.Max(textLeft, (int)Math.Round(iconX - gap));
            var textRect = new Rectangle(textLeft, 0, Math.Max(0, textRight - textLeft), itemBounds.Height);
            return (iconRect, textRect);
        }
    }

    internal void AttachChrome(WindowChrome chrome)
    {
        activeChrome = chrome;
        ApplyNativeFrame();
    }

    internal void ApplyNativeFrame()
    {
        if (!IsHandleCreated) return;
        (activeChrome ?? WindowChrome.SystemDefault()).ApplyDropDownFrame(Handle, out var rounded);
        RoundedWindowFrame = rounded;
    }

    protected override void OnHandleCreated(EventArgs e)
    {
        base.OnHandleCreated(e);
        ApplyNativeFrame();
    }

    protected override void OnOpening(CancelEventArgs e)
    {
        base.OnOpening(e);
        ApplyNativeFrame();
        LayoutItems();
    }

    private void LayoutItems()
    {
        bool highContrast = SystemInformation.HighContrast;
        int outerPad = LogicalToDeviceUnits(OuterPadDip);
        int itemMargin = LogicalToDeviceUnits(ItemMarginDip);
        int minTotalWidth = LogicalToDeviceUnits(MinTotalWidthDip);
        int minItemHeight = LogicalToDeviceUnits(MinItemHeightDip);
        int separatorHeight = LogicalToDeviceUnits(SeparatorHeightDip);
        int horizontalChrome = LogicalToDeviceUnits(OuterPadDip * 2 + LeftPadDip + IconSizeDip + IconGapDip + RightPadDip);
        int verticalPad = LogicalToDeviceUnits(ItemVerticalPadDip);

        SuspendLayout();
        try
        {
            MinimumSize = Size.Empty;
            MaximumSize = Size.Empty;
            Padding = new Padding(0, outerPad, 0, outerPad);

            int totalWidth = minTotalWidth;
            int maxTextHeight = 0;

            if (highContrast)
            {
                var hcPad = new Padding(LogicalToDeviceUnits(12), LogicalToDeviceUnits(7),
                    LogicalToDeviceUnits(12), LogicalToDeviceUnits(7));
                foreach (ToolStripItem item in Items)
                {
                    if (item is ToolStripSeparator) continue;
                    item.Padding = hcPad;
                    item.AutoSize = true;
                    var preferred = item.GetPreferredSize(Size.Empty);
                    totalWidth = Math.Max(totalWidth, preferred.Width + outerPad * 2);
                    maxTextHeight = Math.Max(maxTextHeight, preferred.Height);
                }
            }
            else
            {
                const TextFormatFlags measureFlags = TextFormatFlags.SingleLine
                    | TextFormatFlags.NoPrefix
                    | TextFormatFlags.NoPadding;
                foreach (ToolStripItem item in Items)
                {
                    if (item is ToolStripSeparator) continue;
                    item.Padding = Padding.Empty;
                    var measured = TextRenderer.MeasureText(item.Text ?? "", Font, Size.Empty, measureFlags);
                    totalWidth = Math.Max(totalWidth, measured.Width + horizontalChrome);
                    maxTextHeight = Math.Max(maxTextHeight, measured.Height);
                }
            }

            int itemHeight = highContrast
                ? Math.Max(minItemHeight, maxTextHeight)
                : Math.Max(minItemHeight, maxTextHeight + verticalPad);

            ToolStripItem? firstVisible = null;
            ToolStripItem? lastVisible = null;
            foreach (ToolStripItem item in Items)
            {
                if (!item.Available) continue;
                firstVisible ??= item;
                lastVisible = item;
            }

            foreach (ToolStripItem item in Items)
            {
                int height = item is ToolStripSeparator ? separatorHeight : itemHeight;
                int topMargin = ReferenceEquals(item, firstVisible) ? outerPad : itemMargin;
                int bottomMargin = ReferenceEquals(item, lastVisible) ? outerPad : itemMargin;
                item.Margin = new Padding(0, topMargin, 0, bottomMargin);
                item.AutoSize = false;
                item.Size = new Size(totalWidth, height);
            }

            MinimumSize = new Size(totalWidth, 0);
            MaximumSize = new Size(totalWidth, 0);
        }
        finally
        {
            ResumeLayout(true);
        }
    }

    protected override void Dispose(bool disposing)
    {
        if (disposing) menuFont.Dispose();
        base.Dispose(disposing);
    }
}

/*
 * Cage workbench browser display, based on noVNC Display.
 * Licensed under MPL 2.0 (see LICENSE.txt)
 */

import CageDisplay, { cagePixelScale, cageSampling } from './cage-display.js';

export default class CageBrowserDisplay extends CageDisplay {
    autoscale(containerWidth, containerHeight) {
        // A framebuffer sized for this pane is shown one pixel per screen
        // pixel. Any other, before or during a resize, follows the pane in the
        // same layout pass, even before the guest receives its resize request.
        const exact = cagePixelScale(this.width, this.height, containerWidth, containerHeight);
        this._target.style.width = exact ? this.width * exact + 'px' : '100%';
        this._target.style.height = exact ? this.height * exact + 'px' : '100%';
        cageSampling(this._target, exact);
    }

    absX(x) {
        const width = this._target.getBoundingClientRect().width;
        return width ? Math.floor(x * this.width / width) : 0;
    }

    absY(y) {
        const height = this._target.getBoundingClientRect().height;
        return height ? Math.floor(y * this.height / height) : 0;
    }

    resize(width, height) {
        if (!this.width || !this.height) {
            super.resize(width, height);
            return;
        }
        if (this.width === width && this.height === height) return;

        const previous = this._backbuffer;
        const next = document.createElement('canvas');
        next.width = width;
        next.height = height;
        const ctx = next.getContext('2d');

        // The decoder needs the unscaled overlap for incremental updates and CopyRect.
        ctx.drawImage(previous, 0, 0);

        // Reuse the retiring buffer to hold the last presented frame while
        // resizing the canvas. Its edges stay painted until the refresh arrives.
        this._drawCtx.globalCompositeOperation = 'copy';
        this._drawCtx.drawImage(this._target, 0, 0);
        this._target.width = width;
        this._target.height = height;
        this._targetCtx.drawImage(previous, 0, 0, width, height);
        previous.width = 0;

        this._backbuffer = next;
        this._drawCtx = ctx;
        this._prevDrawStyle = '';
        this._fbWidth = this._viewportLoc.w = width;
        this._fbHeight = this._viewportLoc.h = height;
    }
}

/*
 * Cage workbench display, based on noVNC Display.
 * Licensed under MPL 2.0 (see LICENSE.txt)
 */

import Display from './display.js';

const screenRatio = () =>
    (typeof window !== 'undefined' && window.devicePixelRatio > 0 ? window.devicePixelRatio : 1);

// The scale at which each framebuffer pixel covers one screen pixel, or 0
// when the framebuffer was not sized for this container on this screen. The
// workbench sizes it as floor(container × ratio): up to one screen pixel short.
export function cagePixelScale(fbWidth, fbHeight, width, height, ratio = screenRatio()) {
    if (!(fbWidth > 0 && fbHeight > 0 && width > 0 && height > 0 && ratio > 0)) return 0;
    const fits = (fb, css) => fb <= css * ratio + 0.01 && fb >= css * ratio - 2;
    return fits(fbWidth, width) && fits(fbHeight, height) ? 1 / ratio : 0;
}

// A browser smooths a canvas whose size or position falls between screen
// pixels, as at 125% or 150% scaling or in a pane of odd width, and the whole
// picture blurs. Nearest sampling keeps a 1:1 framebuffer sharp; a scaled one
// (a resize in progress, a fixed density) keeps smoothing.
export function cageSampling(canvas, exact) {
    const value = exact ? 'pixelated' : '';
    if (canvas.style.imageRendering !== value) canvas.style.imageRendering = value;
}

export default class CageDisplay extends Display {
    autoscale(containerWidth, containerHeight) {
        const vp = this._viewportLoc;
        const exact = cagePixelScale(vp.w, vp.h, containerWidth, containerHeight);
        if (exact) this._rescale(exact);
        else super.autoscale(containerWidth, containerHeight);
        cageSampling(this._target, exact);
    }
}

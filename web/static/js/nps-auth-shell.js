(function () {
    'use strict';

    var frame = document.getElementById('auth-network-background');
    if (!frame) return;

    function background() {
        try {
            var api = frame.contentWindow && frame.contentWindow.NPS_BG;
            return api && typeof api.setPointer === 'function' ? api : null;
        } catch (error) {
            return null;
        }
    }

    function moveSpotlight(event) {
        var api = background();
        if (api) api.setPointer(event.clientX, event.clientY, true);
    }

    function clearSpotlight() {
        var api = background();
        if (api) api.setPointer(0, 0, false);
    }

    document.addEventListener('pointermove', moveSpotlight, {passive: true});
    document.addEventListener('pointerleave', clearSpotlight, {passive: true});
    window.addEventListener('blur', clearSpotlight, {passive: true});
}());

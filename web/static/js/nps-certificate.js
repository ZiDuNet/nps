(function (window, $) {
    'use strict';

    function localized(zh, en) {
        return (typeof window.npsIsEnglish === 'function' && window.npsIsEnglish()) ? en : zh;
    }

    function readFileAsBase64(file) {
        return new Promise(function (resolve, reject) {
            if (!file) {
                resolve('');
                return;
            }
            var reader = new FileReader();
            reader.onload = function () {
                var result = String(reader.result || '');
                var comma = result.indexOf(',');
                resolve(comma >= 0 ? result.slice(comma + 1) : result);
            };
            reader.onerror = function () {
                reject(new Error(localized('读取证书文件失败', 'Failed to read the certificate file')));
            };
            reader.readAsDataURL(file);
        });
    }

    function encodeMaterial(value) {
        value = String(value == null ? '' : value);
        try {
            return window.btoa(unescape(encodeURIComponent(value)));
        } catch (error) {
            try {
                return window.btoa(value);
            } catch (ignored) {
                return '';
            }
        }
    }

    function widgetState(widget) {
        var mode = widget.querySelector('select[name="cert_mode"], select[data-cert-mode]');
        var modeValue = mode ? mode.value : 'path';
        return {
            mode: modeValue,
            pathCert: widget.querySelector('[data-cert-path]'),
            pathKey: widget.querySelector('[data-key-path]'),
            uploadCert: widget.querySelector('[data-cert-upload]'),
            uploadKey: widget.querySelector('[data-key-upload]'),
            pasteCert: widget.querySelector('[data-cert-paste]'),
            pasteKey: widget.querySelector('[data-key-paste]')
        };
    }

    function readMaterial(state) {
        var mode = state.mode === 'upload' ? 'upload' : (state.mode === 'paste' ? 'paste' : 'path');
        var certPromise;
        var keyPromise;
        if (mode === 'path') {
            certPromise = Promise.resolve((state.pathCert && state.pathCert.value) || '');
            keyPromise = Promise.resolve((state.pathKey && state.pathKey.value) || '');
        } else if (mode === 'upload') {
            certPromise = readFileAsBase64(state.uploadCert && state.uploadCert.files && state.uploadCert.files[0]);
            keyPromise = readFileAsBase64(state.uploadKey && state.uploadKey.files && state.uploadKey.files[0]);
        } else {
            certPromise = Promise.resolve(encodeMaterial((state.pasteCert && state.pasteCert.value) || ''));
            keyPromise = Promise.resolve(encodeMaterial((state.pasteKey && state.pasteKey.value) || ''));
        }
        return Promise.all([certPromise, keyPromise]).then(function (values) {
            return { mode: mode, cert: values[0], key: values[1] };
        });
    }

    function hostnameFor(widget) {
        var provider = widget.getAttribute('data-hostname-provider');
        if (provider) {
            var providerNode = document.querySelector(provider);
            if (providerNode) return String(providerNode.value || '').trim();
        }
        var rowSelector = widget.getAttribute('data-hostname-row-selector');
        if (rowSelector) {
            var row = widget.closest ? widget.closest('.platform-domain-row') : null;
            var field = row ? row.querySelector(rowSelector) : null;
            if (field) return String(field.value || '').trim();
        }
        var selector = widget.getAttribute('data-hostname-selector');
        if (selector) {
            var field = widget.querySelector(selector);
            if (field) return String(field.value || '').trim();
        }
        return widget.getAttribute('data-hostname') || '';
    }

    function setResult(node, kind, message) {
        if (!node) return;
        node.className = 'certificate-validation-result certificate-validation-result--' + kind;
        node.textContent = message;
        node.hidden = false;
    }

    function updatePanes(widget) {
        var mode = widgetState(widget).mode;
        ['path', 'upload', 'paste'].forEach(function (name) {
            var pane = widget.querySelector('.certificate-mode-pane--' + name);
            if (pane) pane.hidden = name !== mode;
        });
    }

    function validate(widget) {
        var button = widget.querySelector('.certificate-validate');
        var resultNode = widget.querySelector('.certificate-validation-result');
        if (button) button.disabled = true;
        if (resultNode) resultNode.hidden = true;
        var hostname = hostnameFor(widget);
        readMaterial(widgetState(widget)).then(function (material) {
            return $.ajax({
                type: 'POST',
                url: widget.getAttribute('data-validate-url') || '/index/checkcertificate',
                data: {
                    mode: material.mode,
                    cert: material.cert,
                    key: material.key,
                    hostname: hostname,
                    wildcard: widget.getAttribute('data-wildcard') === 'true' ? '1' : '0'
                },
                dataType: 'json'
            });
        }).then(function (response) {
            var data = (response && response.data) || {};
            var valid = Boolean(data.valid);
            var message = data.message || (valid ? localized('证书有效', 'Certificate is valid') : localized('证书无效', 'Certificate is invalid'));
            if (valid && data.expires_at) {
                message += ' · ' + localized('到期时间 ', 'expires ') + data.expires_at;
            }
            setResult(resultNode, valid ? 'success' : 'error', message);
        }).catch(function (error) {
            var response = error && error.responseJSON;
            var message = (response && response.msg) || ((response && response.data && response.data.message)) || localized('校验证书失败，请稍后重试', 'Certificate validation failed');
            setResult(resultNode, 'error', message);
        }).then(function () {
            if (button) button.disabled = false;
        });
    }

    function attach(widget) {
        if (widget.__npsCertificateAttached) return;
        widget.__npsCertificateAttached = true;
        var mode = widget.querySelector('select[name="cert_mode"], select[data-cert-mode]');
        var button = widget.querySelector('.certificate-validate');
        if (mode) mode.addEventListener('change', function () { updatePanes(widget); });
        if (button) button.addEventListener('click', function () { validate(widget); });
        updatePanes(widget);
        widget.prepareCertificate = function () {
            return readMaterial(widgetState(widget)).then(function (material) {
                widget.__npsCertificateMaterial = material;
                return material;
            });
        };
    }

    window.npsCertificatePair = {
        attach: attach,
        attachAll: function (root) {
            var scope = root || document;
            Array.prototype.forEach.call(scope.querySelectorAll('[data-certificate-pair]'), attach);
        },
        prepareIn: function (root) {
            var widgets = Array.prototype.filter.call((root || document).querySelectorAll('[data-certificate-pair]'), function (widget) {
                attach(widget);
                return true;
            });
            return Promise.all(widgets.map(function (widget) {
                return widget.prepareCertificate();
            })).then(function () {
                return widgets;
            });
        }
    };
})(window, window.jQuery);

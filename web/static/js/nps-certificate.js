(function (window, $) {
    'use strict';

    function localized(zh, en) {
        return (typeof window.npsIsEnglish === 'function' && window.npsIsEnglish()) ? en : zh;
    }

    var modeCatalog = {
        path: { zh: '填写路径', en: 'Use path', icon: 'fa-folder-open' },
        upload: { zh: '上传文件', en: 'Upload files', icon: 'fa-upload' },
        paste: { zh: '粘贴内容', en: 'Paste PEM', icon: 'fa-paste' }
    };
    var modeOrder = ['path', 'upload', 'paste'];

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

    function stringHash(value) {
        value = String(value == null ? '' : value);
        var hash = 5381;
        for (var index = 0; index < value.length; index += 1) {
            hash = ((hash << 5) + hash) ^ value.charCodeAt(index);
        }
        return (hash >>> 0).toString(16);
    }

    function modeName(widget) {
        var control = widget.querySelector('input[type="radio"][data-cert-mode]:checked') ||
            widget.querySelector('select[name="cert_mode"], select[data-cert-mode]');
        return control ? String(control.value || 'path') : 'path';
    }

    function availableModes(widget) {
        var select = widget.querySelector('select[name="cert_mode"], select[data-cert-mode]');
        var optionModes = select ? Array.prototype.map.call(select.options, function (option) { return option.value; }) : modeOrder.slice();
        var declared = String(widget.getAttribute('data-certificate-modes') || modeOrder.join(','))
            .split(',')
            .map(function (value) { return value.trim(); })
            .filter(Boolean);
        return modeOrder.filter(function (mode) {
            return declared.indexOf(mode) >= 0 && optionModes.indexOf(mode) >= 0;
        });
    }

    function setMode(widget, mode) {
        var select = widget.querySelector('select[name="cert_mode"], select[data-cert-mode]');
        if (select) {
            select.value = mode;
            select.dispatchEvent(new Event('change', { bubbles: true }));
            return;
        }
        var radio = widget.querySelector('input[type="radio"][data-cert-mode][value="' + mode + '"]');
        if (radio) {
            radio.checked = true;
            radio.dispatchEvent(new Event('change', { bubbles: true }));
        }
    }

    function ensureModeSwitch(widget) {
        var select = widget.querySelector('select[name="cert_mode"], select[data-cert-mode]');
        var nativeControl = select || widget.querySelector('input[type="radio"][data-cert-mode]');
        if (!nativeControl || widget.querySelector('.certificate-mode-switch')) return;
        var modes = availableModes(widget);
        if (modes.length < 2) return;
        var switchNode = document.createElement('div');
        switchNode.className = 'certificate-mode-switch';
        switchNode.setAttribute('role', 'tablist');
        switchNode.setAttribute('aria-label', localized('证书录入方式', 'Certificate input method'));
        modes.forEach(function (mode) {
            var meta = modeCatalog[mode] || modeCatalog.path;
            var button = document.createElement('button');
            button.type = 'button';
            button.className = 'certificate-mode-option';
            button.setAttribute('role', 'tab');
            button.setAttribute('data-mode', mode);
            button.innerHTML = '<i class="fa ' + meta.icon + '" aria-hidden="true"></i><span></span>';
            var label = button.querySelector('span');
            label.textContent = localized(meta.zh, meta.en);
            label.setAttribute('data-i18n-zh', meta.zh);
            label.setAttribute('data-i18n-en', meta.en);
            button.addEventListener('click', function () { setMode(widget, mode); });
            switchNode.appendChild(button);
        });
        nativeControl.parentNode.insertBefore(switchNode, nativeControl);
        if (select) {
            select.classList.add('certificate-mode-select--native');
            select.setAttribute('aria-hidden', 'true');
            select.tabIndex = -1;
        }
    }

    function widgetState(widget) {
        return {
            mode: modeName(widget),
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
            var scopedField = widget.querySelector(selector);
            if (scopedField) return String(scopedField.value || '').trim();
        }
        return widget.getAttribute('data-hostname') || '';
    }

    function materialHasPayload(material) {
        return Boolean(material && (String(material.cert || '').trim() !== '' || String(material.key || '').trim() !== ''));
    }

    function materialSignature(widget, material) {
        material = material || { mode: 'path', cert: '', key: '' };
        var cert = String(material.cert || '');
        var key = String(material.key || '');
        return [
            material.mode,
            hostnameFor(widget),
            String(cert.length),
            stringHash(cert),
            String(key.length),
            stringHash(key)
        ].join(':');
    }

    function shortFingerprint(value) {
        value = String(value || '');
        return value.length > 24 ? value.slice(0, 24) + '…' : value;
    }

    function formatBytes(bytes) {
        bytes = Number(bytes) || 0;
        if (bytes < 1024) return bytes + ' B';
        if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
        return (bytes / 1024 / 1024).toFixed(1) + ' MB';
    }

    function fileSummary(input) {
        if (!input || !input.files || !input.files.length) return '';
        return Array.prototype.map.call(input.files, function (file) {
            return file.name + ' (' + formatBytes(file.size) + ')';
        }).join(', ');
    }

    function ensureSummary(widget) {
        if (String(widget.getAttribute('data-certificate-summary') || '').toLowerCase() === 'false') return null;
        var summary = widget.querySelector('[data-certificate-summary]');
        if (summary) return summary;
        summary = document.createElement('section');
        summary.className = 'certificate-summary certificate-summary--muted';
        summary.setAttribute('data-certificate-summary', '');
        summary.innerHTML = [
            '<div class="certificate-summary__head"><div><span class="certificate-summary__eyebrow"></span><strong class="certificate-summary__status"></strong></div><span class="certificate-summary__fingerprint" title=""></span></div>',
            '<p class="certificate-summary__message"></p>',
            '<dl class="certificate-summary__grid">',
            '<div><dt>来源</dt><dd data-summary="source">-</dd></div>',
            '<div><dt>证书</dt><dd data-summary="cert">-</dd></div>',
            '<div><dt>私钥</dt><dd data-summary="key">-</dd></div>',
            '<div><dt>到期时间</dt><dd data-summary="expires">-</dd></div>',
            '<div><dt>剩余天数</dt><dd data-summary="days">-</dd></div>',
            '<div><dt>主题</dt><dd data-summary="subject">-</dd></div>',
            '<div><dt>SAN</dt><dd data-summary="sans">-</dd></div>',
            '<div><dt>颁发者</dt><dd data-summary="issuer">-</dd></div>',
            '<div><dt>密钥算法</dt><dd data-summary="algorithm">-</dd></div>',
            '<div><dt>证书链</dt><dd data-summary="chain">-</dd></div>',
            '</dl>'
        ].join('');
        var validation = widget.querySelector('.certificate-validation-result');
        if (validation && validation.parentNode === widget) {
            widget.insertBefore(summary, validation);
        } else {
            widget.appendChild(summary);
        }
        return summary;
    }

    function summaryLocations(widget, material) {
        var state = widgetState(widget);
        if (material.mode === 'path') {
            return {
                cert: (state.pathCert && state.pathCert.value) || '-',
                key: (state.pathKey && state.pathKey.value) || '-'
            };
        }
        if (material.mode === 'upload') {
            return {
                cert: fileSummary(state.uploadCert) || localized('尚未选择证书文件', 'No certificate file selected'),
                key: fileSummary(state.uploadKey) || localized('尚未选择私钥文件', 'No private-key file selected')
            };
        }
        return {
            cert: material.cert ? localized('已粘贴证书内容', 'Certificate content pasted') + ' · ' + formatBytes(material.cert.length * 3 / 4) : '-',
            key: material.key ? localized('已粘贴私钥内容', 'Private-key content pasted') + ' · ' + formatBytes(material.key.length * 3 / 4) : '-'
        };
    }

    function renderSummary(widget, options) {
        var summary = ensureSummary(widget);
        if (!summary) return;
        options = options || {};
        var tone = options.tone || 'muted';
        summary.className = 'certificate-summary certificate-summary--' + tone;
        var status = summary.querySelector('.certificate-summary__status');
        var message = summary.querySelector('.certificate-summary__message');
        var fingerprint = summary.querySelector('.certificate-summary__fingerprint');
        var eyebrow = summary.querySelector('.certificate-summary__eyebrow');
        if (eyebrow) eyebrow.textContent = options.eyebrow || localized('证书信息', 'Certificate information');
        if (status) status.textContent = options.status || localized('未配置', 'Not configured');
        if (message) message.textContent = options.message || '';
        if (fingerprint) {
            fingerprint.textContent = options.fingerprint ? shortFingerprint(options.fingerprint) : '';
            fingerprint.title = options.fingerprint || '';
        }
        var locations = options.locations || { cert: '-', key: '-' };
        var fields = {
            source: options.source || '-',
            cert: locations.cert,
            key: locations.key,
            expires: options.expires || '-',
            days: options.days == null || options.days === '' ? '-' : String(options.days),
            subject: options.subject || '-',
            sans: options.sans || '-',
            issuer: options.issuer || '-',
            algorithm: options.algorithm || '-',
            chain: options.chain || '-'
        };
        Object.keys(fields).forEach(function (key) {
            var node = summary.querySelector('[data-summary="' + key + '"]');
            if (node) node.textContent = fields[key];
        });
    }

    function renderPendingSummary(widget, material) {
        if (!materialHasPayload(material)) return;
        var meta = modeCatalog[material.mode] || modeCatalog.path;
        renderSummary(widget, {
            tone: 'warning',
            eyebrow: localized('待校验证书', 'Certificate pending validation'),
            status: localized('等待校验', 'Validation required'),
            message: localized('请点击“校验证书”。校验通过后才能保存。', 'Click "Validate" first. Saving is blocked until validation succeeds.'),
            source: localized(meta.zh, meta.en),
            locations: summaryLocations(widget, material)
        });
    }

    function renderInspectionSummary(widget, data, material, options) {
        options = options || {};
        var valid = Boolean(data && data.valid);
        var days = data ? data.days_remaining : null;
        var tone = valid ? (Number(days) >= 0 && Number(days) < 15 ? 'warning' : 'success') : 'danger';
        var meta = modeCatalog[material.mode] || modeCatalog.path;
        renderSummary(widget, {
            tone: tone,
            eyebrow: options.eyebrow || localized('当前证书', 'Current certificate'),
            status: data && data.message ? data.message : (valid ? localized('证书有效', 'Certificate valid') : localized('证书无效', 'Certificate invalid')),
            message: valid ? localized('证书与私钥匹配，并已完成有效性校验。', 'The certificate and private key match and passed validation.') : (data && data.message) || localized('证书校验失败', 'Certificate validation failed'),
            source: options.source || localized(meta.zh, meta.en),
            locations: summaryLocations(widget, material),
            subject: data && data.subject,
            issuer: data && data.issuer,
            expires: data && data.expires_at,
            days: days,
            sans: data && Array.isArray(data.sans) ? data.sans.join(', ') : '',
            fingerprint: data && data.fingerprint,
            algorithm: data && data.public_key_algorithm,
            chain: data && data.chain_length ? data.chain_length : ''
        });
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
        Array.prototype.forEach.call(widget.querySelectorAll('.certificate-mode-option'), function (button) {
            var active = button.getAttribute('data-mode') === mode;
            button.classList.toggle('is-active', active);
            button.setAttribute('aria-selected', active ? 'true' : 'false');
            button.tabIndex = active ? 0 : -1;
        });
    }

    function requestInspection(widget, material) {
        return $.ajax({
            type: 'POST',
            url: widget.getAttribute('data-validate-url') || '/index/checkcertificate',
            data: {
                mode: material.mode,
                cert: material.cert,
                key: material.key,
                hostname: hostnameFor(widget),
                wildcard: widget.getAttribute('data-wildcard') === 'true' ? '1' : '0'
            },
            dataType: 'json'
        }).then(function (response) {
            return (response && response.data) || {};
        });
    }

    function inspectExisting(widget) {
        var state = widgetState(widget);
        var certPath = state.pathCert ? String(state.pathCert.value || '').trim() : '';
        var keyPath = state.pathKey ? String(state.pathKey.value || '').trim() : '';
        if (!certPath && !keyPath) return Promise.resolve(false);
        var material = { mode: 'path', cert: certPath, key: keyPath };
        return requestInspection(widget, material).then(function (data) {
            widget.__npsExistingCertificateInspection = data;
            renderInspectionSummary(widget, data, material, {
                eyebrow: localized('已保存证书', 'Saved certificate'),
                source: localized('已保存的托管文件', 'Saved managed files')
            });
            return data;
        }).catch(function () {
            renderSummary(widget, {
                tone: 'danger',
                eyebrow: localized('已保存证书', 'Saved certificate'),
                status: localized('无法读取证书', 'Certificate unavailable'),
                message: localized('当前证书文件无法读取或校验，请重新上传或粘贴证书。', 'The current certificate files cannot be read or validated. Upload or paste a replacement.'),
                source: localized('已保存的路径', 'Saved paths'),
                locations: { cert: certPath || '-', key: keyPath || '-' }
            });
            return false;
        });
    }

    function renderUnconfiguredSummary(widget) {
        renderSummary(widget, {
            tone: 'muted',
            eyebrow: localized('证书信息', 'Certificate information'),
            status: localized('未配置', 'Not configured'),
            message: localized('当前规则尚未提供证书。HTTPS 或双协议规则请上传或粘贴有效证书并完成校验。', 'No certificate is configured. For HTTPS or dual-protocol rules, upload or paste a valid certificate and validate it.'),
            source: '-',
            locations: { cert: '-', key: '-' }
        });
    }

    function refreshCurrentSummary(widget) {
        return readMaterial(widgetState(widget)).then(function (material) {
            if (materialHasPayload(material)) {
                renderPendingSummary(widget, material);
                return;
            }
            return inspectExisting(widget).then(function (loaded) {
                if (!loaded) renderUnconfiguredSummary(widget);
            });
        });
    }

    function validate(widget) {
        var button = widget.querySelector('.certificate-validate');
        var resultNode = widget.querySelector('.certificate-validation-result');
        if (button) button.disabled = true;
        if (resultNode) resultNode.hidden = true;
        return readMaterial(widgetState(widget)).then(function (material) {
            if (!materialHasPayload(material)) {
                setResult(resultNode, 'error', localized('请先填写、粘贴或选择证书与私钥。', 'Provide, paste, or select both the certificate and private key first.'));
                return false;
            }
            return requestInspection(widget, material).then(function (data) {
                var valid = Boolean(data.valid);
                var message = data.message || (valid ? localized('证书有效', 'Certificate is valid') : localized('证书无效', 'Certificate is invalid'));
                if (valid && data.expires_at) {
                    message += ' · ' + localized('到期时间 ', 'expires ') + data.expires_at;
                }
                setResult(resultNode, valid ? 'success' : 'error', message);
                renderInspectionSummary(widget, data, material);
                if (valid) {
                    widget.__npsCertificateValidation = {
                        signature: materialSignature(widget, material),
                        data: data
                    };
                } else {
                    widget.__npsCertificateValidation = null;
                }
                return valid;
            });
        }).catch(function (error) {
            var response = error && error.responseJSON;
            var message = (response && response.msg) || ((response && response.data && response.data.message)) ||
                (error && error.message) || localized('校验证书失败，请稍后重试', 'Certificate validation failed');
            widget.__npsCertificateValidation = null;
            setResult(resultNode, 'error', message);
            renderSummary(widget, {
                tone: 'danger',
                eyebrow: localized('证书校验失败', 'Certificate validation failed'),
                status: localized('校验失败', 'Validation failed'),
                message: message
            });
            return false;
        }).then(function (result) {
            if (button) button.disabled = false;
            return result;
        });
    }

    function markValidationStale(widget) {
        widget.__npsCertificateValidation = null;
    }

    function bindMaterialInputs(widget) {
        var pendingTimer;
        var updatePending = function () {
            window.clearTimeout(pendingTimer);
            pendingTimer = window.setTimeout(function () {
                refreshCurrentSummary(widget).catch(function () { return false; });
            }, 120);
        };
        Array.prototype.forEach.call(widget.querySelectorAll('[data-cert-path], [data-key-path], [data-cert-upload], [data-key-upload], [data-cert-paste], [data-key-paste]'), function (input) {
            input.addEventListener(input.type === 'file' ? 'change' : 'input', function () {
                markValidationStale(widget);
                updatePending();
            });
        });
        var providerSelector = widget.getAttribute('data-hostname-provider');
        var provider = providerSelector ? document.querySelector(providerSelector) : null;
        if (provider) provider.addEventListener('input', function () { markValidationStale(widget); });
        var rowSelector = widget.getAttribute('data-hostname-row-selector');
        var row = rowSelector && widget.closest ? widget.closest('.platform-domain-row') : null;
        var rowField = row ? row.querySelector(rowSelector) : null;
        if (rowField) rowField.addEventListener('input', function () { markValidationStale(widget); });
    }

    function attach(widget) {
        if (widget.__npsCertificateAttached) return;
        widget.__npsCertificateAttached = true;
        ensureModeSwitch(widget);
        var mode = widget.querySelector('input[type="radio"][data-cert-mode], select[name="cert_mode"], select[data-cert-mode]');
        var button = widget.querySelector('.certificate-validate');
        if (mode) mode.addEventListener('change', function () {
            markValidationStale(widget);
            updatePanes(widget);
            refreshCurrentSummary(widget).catch(function () { return false; });
        });
        if (button) button.addEventListener('click', function () { validate(widget); });
        bindMaterialInputs(widget);
        updatePanes(widget);
        widget.prepareCertificate = function () {
            return readMaterial(widgetState(widget)).then(function (material) {
                widget.__npsCertificateMaterial = material;
                return material;
            });
        };
        widget.prepareForSubmit = function () {
            return widget.prepareCertificate().then(function (material) {
                if (materialHasPayload(material) && (material.mode === 'upload' || material.mode === 'paste')) {
                    var validation = widget.__npsCertificateValidation;
                    if (!validation || validation.signature !== materialSignature(widget, material)) {
                        renderPendingSummary(widget, material);
                        throw new Error(localized('已选择上传或粘贴证书，必须先点击“校验证书”并校验通过后才能保存。', 'Uploaded or pasted certificate material must pass validation before it can be saved.'));
                    }
                }
                return material;
            });
        };
        window.setTimeout(function () {
            inspectExisting(widget).catch(function () { return false; });
        }, 0);
    }

    window.npsCertificatePair = {
        attach: attach,
        inspectExisting: inspectExisting,
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
                return widget.prepareForSubmit();
            })).then(function () {
                return widgets;
            });
        }
    };
})(window, window.jQuery);

(function ($) {
	window.switchLanguage = function (lang) {
		var menu = $('#languagemenu');
		if (!menu.length) return false;
		menu.attr('lang', lang);
		if (languages && languages.content) $('body').setLang('');
		return false;
	};

	function xml2json(Xml) {
		var tempvalue, tempJson = {};
		$(Xml).each(function() {
			var tagName = ($(this).attr('id') || this.tagName);
			tempvalue = (this.childElementCount == 0) ? this.textContent : xml2json($(this).children());
			switch ($.type(tempJson[tagName])) {
				case 'undefined':
					tempJson[tagName] = tempvalue;
					break;
				case 'object':
					tempJson[tagName] = Array(tempJson[tagName]);
				case 'array':
					tempJson[tagName].push(tempvalue);
			}
		});
		return tempJson;
	}

	function setCookie (c_name, value, expiredays) {
		var exdate = new Date();
		exdate.setDate(exdate.getDate() + expiredays);
		var basePath = (window.nps && typeof window.nps.web_base_url === 'string') ? window.nps.web_base_url : '';
		var cookiePath = basePath || '/';
		if (cookiePath.charAt(cookiePath.length - 1) !== '/') cookiePath += '/';
		document.cookie = encodeURIComponent(c_name) + '=' + encodeURIComponent(value)
			+ ((expiredays == null) ? '' : '; expires=' + exdate.toUTCString())
			+ '; path=' + cookiePath + '; SameSite=Lax';
	}

	function getCookie (c_name) {
		if (document.cookie.length > 0) {
			var c_start = document.cookie.indexOf(c_name + '=');
			if (c_start != -1) {
				c_start = c_start + c_name.length + 1;
				var c_end = document.cookie.indexOf(';', c_start);
				if (c_end == -1) c_end = document.cookie.length;
				return decodeURIComponent(document.cookie.substring(c_start, c_end));
			}
		}
		return null;
	}

	function getLanguagePreference () {
		try {
			return window.localStorage.getItem('nps-language');
		} catch (error) {
			return null;
		}
	}

	function saveLanguagePreference (value) {
		try {
			window.localStorage.setItem('nps-language', value);
		} catch (error) {
			// Cookie persistence below remains the fallback for restricted storage.
		}
	}

	function setchartlang (langobj,chartobj) {
		if ($.type(langobj) == 'string') return langobj;
		if (!langobj || $.type(langobj) != 'object') return undefined;

		// Locale leaves are objects such as {"zh-CN": "入口", "en-US": "In"}.
		// Resolve them before checking chartobj so empty chart labels can still be
		// populated.
		var translated = langobj[languages['current']] || langobj[languages['default']];
		if ($.type(translated) == 'string') return {'value': translated};
		var chartType = $.type(chartobj);
		if (!chartobj || (chartType != 'object' && chartType != 'array')) return undefined;

		var changed = false;
		for (var key in langobj) {
			if (!Object.prototype.hasOwnProperty.call(langobj, key)) continue;
			if (key == languages['current'] || key == languages['default']) continue;
			if (!Object.prototype.hasOwnProperty.call(chartobj, key)) continue;
			var children = setchartlang(langobj[key], chartobj[key]);
			if ($.type(children) == 'string' && $.type(chartobj[key]) == 'string') {
				chartobj[key] = children;
				changed = true;
			} else if (children && Object.prototype.hasOwnProperty.call(children, 'value')) {
				chartobj[key] = children.value;
				changed = true;
			} else if (children === true) {
				changed = true;
			}
		}
		return changed ? true : undefined;
	}

	$.fn.cloudLang = function () {
		$.ajax({
			type: 'GET',
			url: window.nps.web_base_url + '/static/page/languages.xml?v=' + (window.nps.version || Date.now()),
			dataType: 'xml',
			success: function (xml) {
				languages['content'] = xml2json($(xml).children())['content'];
				languages['menu'] = languages['content']['languages'];
				languages['default'] = languages['content']['default'];
				// Keep Chinese as the first-run default; only an explicit user cookie
				// overrides it.
					languages['navigator'] = (getCookie ('lang-v2') || getLanguagePreference() || languages['default']);
					for(var key in languages['menu']){
						if (!Object.prototype.hasOwnProperty.call(languages['menu'], key)) continue;
						if (key.toLowerCase() == languages['navigator'].toLowerCase()) languages['current'] = key;
						if ($('#languagemenu').next().find('li[lang="' + key + '"]').length) continue;
						$('#languagemenu').next().append('<li lang="' + key + '"><a href="#">' + languages['menu'][key] +'</a></li>');
					}
				$('#languagemenu').attr('lang',(languages['current'] || languages['default']));
				$('body').setLang ('');
			}
		});
	};

	$.fn.setLang = function (dom) {
		if (!languages || !languages['content']) return false;
		languages['current'] = $('#languagemenu').attr('lang');
		if ( dom == '' ) {
			$('#languagemenu span').text(' ' + languages['menu'][languages['current']]);
			if (languages['current'] != getCookie('lang-v2')) setCookie('lang-v2', languages['current']);
			saveLanguagePreference(languages['current']);
			if($("#table").length>0) $('#table').npsTable('refreshOptions', { 'locale': languages['current']});
		}
		$.each($(dom + ' [langtag]'), function (i, item) {
			var index = $(item).attr('langtag');
			var string = languages['content'][index.toLowerCase()];
			switch ($.type(string)) {
				case 'string':
					break;
				case 'array':
					string = string[Math.floor((Math.random()*string.length))];
				case 'object':
					string = (string[languages['current']] || string[languages['default']] || null);
					break;
				default:
					string = 'Missing language string "' + index + '"';
					$(item).css('background-color','#ffeeba');
			}
			if($.type($(item).attr('placeholder')) == 'undefined') {
				$(item).text(string);
			} else {
				$(item).attr('placeholder', string);
			}
		});
		$.each($(dom + ' [data-i18n-zh]'), function (i, item) {
			var language = languages['current'] === 'en-US' ? 'en' : 'zh';
			$(item).text($(item).attr('data-i18n-' + language));
		});
		var language = languages['current'] === 'en-US' ? 'en' : 'zh';
		$.each($(dom + ' [data-placeholder-zh]'), function (i, item) {
			$(item).attr('placeholder', $(item).attr('data-placeholder-' + language));
		});
		$.each($(dom + ' [data-aria-label-zh]'), function (i, item) {
			$(item).attr('aria-label', $(item).attr('data-aria-label-' + language));
		});
		$.each($(dom + ' [data-title-zh]'), function (i, item) {
			$(item).attr('title', $(item).attr('data-title-' + language));
		});
		npsRefreshToggleLabels(dom);
		npsDecorateForms(dom);
		npsDecorateDetailControls(dom);
		// Column titles are generated before the asynchronous language file may
		// finish loading; rebuild the filter menu after labels are translated.
		var tableInstance = $('#table').data('nps.table');
		if (tableInstance && tableInstance.options.showColumns && typeof tableInstance.renderColumnsMenu === 'function') {
			tableInstance.renderColumnsMenu();
		}

		if ( !$.isEmptyObject(chartdatas) ) {
			setchartlang(languages['content']['charts'],chartdatas);
			if (typeof applyDashboardChartLabels === 'function') {
				applyDashboardChartLabels();
			}
			for(var key in chartdatas){
				if (!Object.prototype.hasOwnProperty.call(chartdatas, key)) continue;
				if ($('#'+key).length == 0) continue;
				if ($.type(chartdatas[key]) != 'object') continue;
				if (!charts[key] || (typeof charts[key].isDisposed === 'function' && charts[key].isDisposed())) {
					charts[key] = echarts.init(document.getElementById(key));
				}
				charts[key].setOption(chartdatas[key], true);
			}
			if (typeof applyChartTheme === 'function') {
				applyChartTheme(document.documentElement.getAttribute('data-theme') || 'light');
			}
		}
	}

})(jQuery);

$(document).ready(function () {
	$('body').cloudLang();
	$('body').on('click','li[lang] a',function(e){
		e.preventDefault();
		window.switchLanguage($(this).closest('li').attr('lang'));
	});
});

var languages = {};
var charts = {};
var chartdatas = {};
var postsubmit;

var npsFieldHints = {
    remark: ['用于在列表中识别这条配置。', 'A label used to identify this configuration.'],
    vkey: ['留空将由系统自动生成验证密钥。', 'Leave blank to generate a verification key automatically.'],
    username: ['用于登录管理面板的账号名称。', 'The account name used to sign in.'],
    password: ['用于登录或代理认证的密码。', 'The password used for sign-in or proxy authentication.'],
    user_id: ['将客户端分配给指定的面板用户。', 'Assign this client to a panel user.'],
    flow_limit: ['单个客户端允许使用的总流量，0 或留空表示不限。', 'Total traffic allowed for this client; 0 or blank means unlimited.'],
    rate_limit: ['限制客户端传输速率，单位为 KB/S。', 'Limit client transfer speed in KB/S.'],
    max_conn: ['限制客户端同时连接数，0 或留空表示不限。', 'Maximum concurrent connections; 0 or blank means unlimited.'],
    max_tunnel: ['限制客户端创建的隧道数量，0 或留空表示不限。', 'Maximum tunnels for this client; 0 or blank means unlimited.'],
    u: ['可选的 HTTP 基础认证用户名。', 'Optional HTTP Basic Authentication username.'],
    p: ['可选的 HTTP 基础认证密码。', 'Optional HTTP Basic Authentication password.'],
    compress: ['启用后可减少传输体积，但会增加 CPU 使用。', 'Reduces transfer size at the cost of CPU usage.'],
    crypt: ['加密客户端与服务端之间的代理数据。', 'Encrypt proxy data between the client and server.'],
    web_username: ['客户端 Web 服务使用的认证用户名。', 'Authentication username for the client Web service.'],
    web_password: ['客户端 Web 服务使用的认证密码。', 'Authentication password for the client Web service.'],
    ipwhitepass: ['启用 IP 白名单时使用的访问口令。', 'Access password used when IP allowlist is enabled.'],
    ipwhitelist: ['每行填写一个允许访问的精确 IP。', 'Enter one exact allowed IP per line.'],
    blackiplist: ['每行填写一个禁止访问的精确 IP。', 'Enter one exact blocked IP per line.'],
    expire_time: ['到期后配置将不可用；留空表示永不过期。', 'The configuration expires at this time; blank means no expiry.'],
    type: ['选择此隧道要使用的代理协议。', 'Select the proxy protocol for this tunnel.'],
    client_id: ['选择承载此隧道的客户端。', 'Select the client that will carry this tunnel.'],
    server_ip: ['指定服务端监听地址，通常保持 0.0.0.0。', 'Server bind address; usually keep 0.0.0.0.'],
    port: ['服务端对外监听的端口，需确保防火墙已放行。', 'Public listening port; make sure the firewall allows it.'],
    target: ['内网目标地址，例如 127.0.0.1:8080；多个目标可换行填写。', 'Internal target, e.g. 127.0.0.1:8080; enter multiple targets on separate lines.'],
    local_path: ['文件代理在客户端使用的本地目录。', 'Local directory used by the file proxy on the client.'],
    strip_pre: ['从请求路径中移除指定的前缀。', 'Remove the specified prefix from the request path.'],
    local_proxy: ['启用后由客户端本地代理访问目标。', 'Let the client local proxy access the target.'],
    password: ['私密代理或 HTTP 认证使用的访问密钥。', 'Access key used by the private proxy or HTTP auth.'],
    host: ['客户端请求使用的主机名；可填写主域名或子域名。', 'Hostname used by clients; enter a root or subdomain.'],
    scheme: ['选择 HTTP、HTTPS，或同时接受两种协议。', 'Choose HTTP, HTTPS, or accept both protocols.'],
    cert_file_path: ['粘贴证书内容，或填写证书文件路径。', 'Paste the certificate or enter its file path.'],
    key_file_path: ['粘贴私钥内容，或填写私钥文件路径。', 'Paste the private key or enter its file path.'],
    location: ['可选的 URL 路径前缀，用于请求路由。', 'Optional URL path prefix used for request routing.'],
    header: ['每行填写一个需要改写或追加的请求头。', 'Enter one request header to rewrite or append per line.'],
    hostchange: ['转发请求时替换目标 Host 请求头。', 'Replace the Host header sent to the target.'],
    globalBlackIpList: ['全局禁止访问的精确 IP，每行一条。', 'Globally blocked exact IPs, one per line.'],
    serverUrl: ['客户端连接服务端时使用的地址。', 'The address clients use to connect to the server.']
};

function npsDecorateForms(dom) {
    var scope = dom ? dom + ' ' : '';
    $(scope + '[required]').each(function () {
        var group = this.closest('.form-group, .form-field');
        var label = group ? group.querySelector('label') : null;
        if (label && !label.querySelector('.required-mark')) {
            var mark = document.createElement('span');
            mark.className = 'required-mark';
            mark.setAttribute('aria-hidden', 'true');
            mark.textContent = '*';
            label.appendChild(mark);
        }
    });
    $(scope + 'input, ' + scope + 'textarea, ' + scope + 'select').each(function () {
        if (this.type === 'hidden' || this.type === 'checkbox' || this.type === 'radio') return;
        var hint = npsFieldHints[this.name];
        var group = this.closest('.form-group, .form-field');
        if (this.closest('.login-form-wrap')) return;
        if (!hint || !group || group.querySelector('.help-block, .form-field-hint')) return;
        var node = document.createElement('span');
        node.className = 'form-field-hint';
        node.setAttribute('data-hint-zh', hint[0]);
        node.setAttribute('data-hint-en', hint[1]);
        node.textContent = languages['current'] === 'en-US' ? hint[1] : hint[0];
        this.parentNode.appendChild(node);
    });
    $(scope + '[data-hint-zh]').each(function () {
        $(this).text(languages['current'] === 'en-US' ? $(this).attr('data-hint-en') : $(this).attr('data-hint-zh'));
    });
}

function npsSetToggleLabel(element, checked) {
    var $label = $(element);
    if (!$label.length) return;
    var english = npsIsEnglish();
    $label.attr('data-toggle-state', checked ? 'yes' : 'no')
        .text(checked ? (english ? 'Yes' : '是') : (english ? 'No' : '否'));
}

function npsRefreshToggleLabels(dom) {
    var scope = dom ? dom + ' ' : '';
    $(scope + '.toggle-label[data-toggle-state]').each(function () {
        npsSetToggleLabel(this, $(this).attr('data-toggle-state') === 'yes');
    });
}

function langreply(langstr) {
    if (!languages || !languages['content'] || !languages['content']['reply']) return langstr;
    var langobj = languages['content']['reply'][langstr.replace(/[\s,\.\?]*/g,"").toLowerCase()];
    if ($.type(langobj) == 'undefined') return langstr
    langobj = (langobj[languages['current']] || langobj[languages['default']] || langstr);
    return langobj
}

function npsIsEnglish() {
    return typeof languages !== 'undefined' && languages['current'] === 'en-US';
}

function npsEscapeHtml(value) {
    var element = document.createElement('div');
    element.textContent = value == null ? '' : String(value);
    return element.innerHTML
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#39;')
        .replace(/`/g, '&#x60;');
}

function npsBooleanMarkup(value) {
    var zh = value ? '是' : '否';
    var en = value ? 'Yes' : 'No';
    return '<span data-i18n-zh="' + zh + '" data-i18n-en="' + en + '">' + (npsIsEnglish() ? en : zh) + '</span>';
}

function npsRequestErrorMessage() {
    return npsIsEnglish()
        ? 'The request failed. Check the connection and try again.'
        : '请求失败，请检查连接后重试。';
}

function npsNotify(type, msg) {
    msg = msg == null ? '' : String(msg);
    if (typeof toastr !== 'undefined') {
        var opts = { positionClass: 'toast-top-center', timeOut: 3000, closeButton: true };
        var safeMsg = npsEscapeHtml(msg);
        if (type === 'error') toastr.error(safeMsg, '', opts);
        else if (type === 'success') toastr.success(safeMsg, '', opts);
        else if (type === 'warning') toastr.warning(safeMsg, '', opts);
        else toastr.info(safeMsg, '', opts);
    } else {
        var previous = document.querySelector('.nps-inline-notice');
        if (previous) previous.remove();
        var notice = document.createElement('div');
        notice.className = 'nps-inline-notice nps-inline-notice--' + type;
        notice.setAttribute('role', type === 'error' ? 'alert' : 'status');
        notice.setAttribute('aria-live', type === 'error' ? 'assertive' : 'polite');
        var icon = document.createElement('i');
        icon.className = type === 'success' ? 'fa fa-check-circle' : type === 'warning' ? 'fa fa-exclamation-triangle' : type === 'error' ? 'fa fa-exclamation-circle' : 'fa fa-info-circle';
        icon.setAttribute('aria-hidden', 'true');
        var message = document.createElement('span');
        message.textContent = msg;
        notice.appendChild(icon);
        notice.appendChild(message);
        document.body.appendChild(notice);
        window.setTimeout(function () {
            notice.classList.add('is-visible');
        }, 0);
        window.setTimeout(function () {
            notice.classList.remove('is-visible');
            window.setTimeout(function () { notice.remove(); }, 180);
        }, 3200);
    }
}

function npsTableState(kind, loading) {
    var english = npsIsEnglish();
    var copy = {
        client: english ? ['No clients yet', 'Create a client to connect a local network to this server.', 'desktop'] : ['还没有客户端', '创建客户端后即可连接内网服务。', 'desktop'],
        tunnel: english ? ['No tunnels match this view', 'Create a tunnel or adjust the current search.', 'exchange-alt'] : ['当前没有匹配的隧道', '可以新建隧道，或调整当前搜索条件。', 'exchange-alt'],
        host: english ? ['No host rules yet', 'Create a host rule to route domain traffic.', 'globe'] : ['还没有域名规则', '创建域名规则后即可转发站点流量。', 'globe'],
        user: english ? ['No users yet', 'Create a user to delegate management access.', 'users'] : ['还没有用户', '创建用户后即可分配管理权限。', 'users']
    }[kind] || (english ? ['No records found', 'Adjust the search or create a new record.', 'inbox'] : ['没有可显示的记录', '可以调整搜索条件或创建新的记录。', 'inbox']);
    if (loading) {
        return '<div class="table-loading-state" role="status"><i class="fa fa-circle-notch fa-spin" aria-hidden="true"></i><span>' + (english ? 'Loading records...' : '正在加载记录...') + '</span></div>';
    }
    return '<div class="table-empty-state" role="status"><div class="table-empty-state__content"><i class="fa fa-' + copy[2] + '" aria-hidden="true"></i><strong>' + copy[0] + '</strong><span>' + copy[1] + '</span></div></div>';
}

function npsDecorateDetailControls(scope) {
    var $scope = scope ? $(scope) : $('#table');
    if (!$scope.length) return;
    var $controls = $scope.filter('a.detail-icon').add($scope.find('a.detail-icon'));
    if (!$controls.length) return;
    var english = npsIsEnglish();
    $controls.each(function () {
        var expanded = $(this).closest('tr').next('tr.detail-view').length > 0;
        var label = expanded
            ? (english ? 'Collapse details' : '折叠详情')
            : (english ? 'Expand details' : '展开详情');
        $(this).attr({
            role: 'button',
            tabindex: '0',
            'aria-expanded': expanded ? 'true' : 'false',
            'aria-label': label,
            title: label
        });
        $(this).find('i').attr('aria-hidden', 'true');
    });
}

function npsApplyTableState(table, kind) {
    var apply = function () {
        var source = table && table.element ? table.element : table;
        var $scope = $(source).closest('.nps-table');
        if (!$scope.length) $scope = $('#table').closest('.nps-table');
        var $emptyCell = $scope.find('tbody tr.no-records-found > td');
        if ($emptyCell.length) $emptyCell.html(npsTableState(kind, false));
        npsDecorateDetailControls($scope);
    };
    apply();
    window.setTimeout(apply, 0);
}

function npsFormMessage(key) {
    var chinese = typeof languages === 'undefined' || languages['current'] !== 'en-US';
    var messages = {
        required: chinese ? '请填写必填字段：' : 'Please complete the required fields: ',
        invalid: chinese ? '此项不能为空' : 'This field is required'
    };
    return messages[key];
}

function npsFieldLabel(field) {
    var group = field.closest('.form-group, .form-field');
    var label = group ? group.querySelector('label') : null;
    var text = label ? label.textContent.replace(/[\\*\\s:：]+$/g, '').trim() : '';
    return text || field.getAttribute('aria-label') || field.getAttribute('placeholder') || field.getAttribute('name') || npsFormMessage('invalid');
}

function npsSetFieldValidity(field, valid) {
    var group = field.closest('.form-group, .form-field');
    if (!group) return valid;
    group.classList.toggle('has-field-error', !valid);
    field.classList.toggle('is-invalid', !valid);
    var message = group.querySelector('.field-validation-message');
    if (!valid && !message) {
        message = document.createElement('span');
        message.className = 'field-validation-message';
        message.textContent = npsFormMessage('invalid');
        field.parentNode.appendChild(message);
    }
    if (valid && message) message.remove();
    return valid;
}

function validateNpsForm(form, announce) {
    if (!form) return true;
    var required = form.querySelectorAll('input[required], textarea[required], select[required]');
    var missing = [];
    var first = null;
    for (var i = 0; i < required.length; i++) {
        var field = required[i];
        if (field.type === 'checkbox' || field.type === 'radio' || field.offsetParent === null) continue;
        var valid = !!field.value && field.value.trim() !== '';
        npsSetFieldValidity(field, valid);
        if (!valid) {
            missing.push(npsFieldLabel(field));
            if (!first) first = field;
        }
    }
    if (first) {
        if (announce) npsNotify('warning', npsFormMessage('required') + missing.join(npsIsEnglish() ? ', ' : '、'));
        first.focus();
        return false;
    }
    return true;
}

$(function () {
    npsDecorateForms('');
    $(document).on('keydown.npsDetailA11y', 'a.detail-icon[role="button"]', function (event) {
        if (event.key === ' ' || event.key === 'Spacebar') {
            event.preventDefault();
            $(this).trigger('click');
        }
    });
    $('[required]').on('input change blur', function () {
        if (this.value && this.value.trim() !== '') npsSetFieldValidity(this, true);
    });
});

// A referrer can be supplied by an external page. Only return to a URL that
// belongs to this console; otherwise a successful form submission becomes an
// open redirect.
function npsSafeReturnUrl(referrer) {
    var fallback = (window.nps && window.nps.web_base_url ? window.nps.web_base_url + '/' : '/');
    if (!referrer || !window.location.origin) return fallback;
    try {
        var target = new URL(referrer, window.location.origin);
        if (target.origin === window.location.origin) return target.href;
    } catch (error) {
        // Ignore malformed referrers and use the console root.
    }
    return fallback;
}

function submitform(action, url, postdata) {
    postsubmit = false;
    switch (action) {
        case 'start':
        case 'stop':
        case 'delete':
		case 'copy':
            var confirmCatalog = languages && languages['content'] && languages['content']['confirm'];
            var langobj = confirmCatalog && confirmCatalog[action];
            action = (langobj && (langobj[languages['current']] || langobj[languages['default']]))
                || (npsIsEnglish() ? 'Are you sure you want to ' + action + ' it?' : '确定要执行此操作吗？');
            if (! confirm(action)) return;
            postsubmit = true;
        case 'add':
        case 'edit':
            var form = document.querySelector('form.form-horz');
            if (!validateNpsForm(form, true)) return;
            $.ajax({
                type: "POST",
                url: url,
                data: postdata,
                success: function (res) {
                    npsNotify(res.status ? 'success' : 'error', langreply(res.msg));
                    if (res.status) {
                        if (postsubmit) {
							document.location.reload();
						}else{
                            window.location.href = npsSafeReturnUrl(document.referrer);
                        }
                    }
                },
                error: function () {
                    npsNotify('error', npsRequestErrorMessage());
                }
            });
			return;
		case 'global':
			var formG = document.querySelector('form.form-horz');
			if (!validateNpsForm(formG, true)) return;
			$.ajax({
				type: "POST",
				url: url,
				data: postdata,
				success: function (res) {
					npsNotify(res.status ? 'success' : 'error', langreply(res.msg));
					if (res.status) {
						document.location.reload();
					}
				},
				error: function () {
					npsNotify('error', npsRequestErrorMessage());
				}
			});
    }
}

function changeunit(limit) {
    limit = Number(limit);
    if (!isFinite(limit)) return "0B";
    if (limit < 0) return "-" + changeunit(-limit);
    var size = "";
    if (limit < 0.1 * 1024) {
        size = limit.toFixed(2) + "B";
    } else if (limit < 0.1 * 1024 * 1024) {
        size = (limit / 1024).toFixed(2) + "KB";
    } else if (limit < 0.1 * 1024 * 1024 * 1024) {
        size = (limit / (1024 * 1024)).toFixed(2) + "MB";
    } else {
        size = (limit / (1024 * 1024 * 1024)).toFixed(2) + "GB";
    }

    var sizeStr = size + "";
    var index = sizeStr.indexOf(".");
    var dou = sizeStr.substr(index + 1, 2);
    if (dou == "00") {
        return sizeStr.substring(0, index) + sizeStr.substr(index + 3, 2);
    }
    return size;
}

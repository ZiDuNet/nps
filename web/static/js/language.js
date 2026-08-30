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
		document.cookie = c_name + '=' + escape(value) + ((expiredays == null) ? '' : ';expires=' + exdate.toGMTString())+ '; path='+window.nps.web_base_url+'/;';
	}

	function getCookie (c_name) {
		if (document.cookie.length > 0) {
			c_start = document.cookie.indexOf(c_name + '=');
			if (c_start != -1) {
				c_start = c_start + c_name.length + 1;
				c_end = document.cookie.indexOf(';', c_start);
				if (c_end == -1) c_end = document.cookie.length;
				return unescape(document.cookie.substring(c_start, c_end));
			}
		}
		return null;
	}

	function setchartlang (langobj,chartobj) {
		if ( $.type (langobj) == 'string' ) return langobj;
		if ( $.type (langobj) == 'chartobj' ) return false;
		var flag = true;
		for (key in langobj) {
			var item = key;
			children = (chartobj.hasOwnProperty(item)) ? setchartlang (langobj[item],chartobj[item]) : setchartlang (langobj[item],undefined);
			switch ($.type(children)) {
				case 'string':
					if ($.type(chartobj[item]) != 'string' ) continue;
				case 'object':
					chartobj[item] = (children['value'] || children);
				default:
					flag = false;
			}
		}
		if (flag) { return {'value':(langobj[languages['current']] || langobj[languages['default']] || 'N/A')}}
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
				languages['navigator'] = (getCookie ('lang-v2') || languages['default']);
				for(var key in languages['menu']){
					if ($('#languagemenu').next().find('li[lang="' + key + '"]').length) continue;
					$('#languagemenu').next().append('<li lang="' + key + '"><a href="#">' + languages['menu'][key] +'</a></li>');
					if ( key.toLowerCase() == languages['navigator'].toLowerCase() ) languages['current'] = key;
				}
				$('#languagemenu').attr('lang',(languages['current'] || languages['default']));
				$('body').setLang ('');
			}
		});
	};

	$.fn.setLang = function (dom) {
		languages['current'] = $('#languagemenu').attr('lang');
		if ( dom == '' ) {
			$('#languagemenu span').text(' ' + languages['menu'][languages['current']]);
			if (languages['current'] != getCookie('lang-v2')) setCookie('lang-v2', languages['current']);
			if($("#table").length>0) $('#table').bootstrapTable('refreshOptions', { 'locale': languages['current']});
		}
		$.each($(dom + ' [langtag]'), function (i, item) {
			var index = $(item).attr('langtag');
			string = languages['content'][index.toLowerCase()];
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
		npsDecorateForms(dom);

		if ( !$.isEmptyObject(chartdatas) ) {
			setchartlang(languages['content']['charts'],chartdatas);
			for(var key in chartdatas){
				if ($('#'+key).length == 0) continue;
				if($.type(chartdatas[key]) == 'object')
				charts[key] = echarts.init(document.getElementById(key));
				charts[key].setOption(chartdatas[key], true);
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
    ipwhitelist: ['每行填写一个允许访问的 IP 或网段。', 'Enter one allowed IP or CIDR range per line.'],
    blackiplist: ['每行填写一个禁止访问的 IP 或网段。', 'Enter one blocked IP or CIDR range per line.'],
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
    globalBlackIpList: ['全局禁止访问的 IP 或网段，每行一条。', 'Globally blocked IPs or CIDR ranges, one per line.'],
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
        if (this.name === 'password' && this.closest('.login-form-wrap')) {
            hint = ['用于登录管理面板的密码。', 'The password used to sign in to the admin panel.'];
        }
        var group = this.closest('.form-group, .form-field');
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

function langreply(langstr) {
    var langobj = languages['content']['reply'][langstr.replace(/[\s,\.\?]*/g,"").toLowerCase()];
    if ($.type(langobj) == 'undefined') return langstr
    langobj = (langobj[languages['current']] || langobj[languages['default']] || langstr);
    return langobj
}

function npsNotify(type, msg) {
    if (typeof toastr !== 'undefined') {
        var opts = { positionClass: 'toast-top-center', timeOut: 3000, closeButton: true };
        if (type === 'error') toastr.error(msg, '', opts);
        else if (type === 'success') toastr.success(msg, '', opts);
        else if (type === 'warning') toastr.warning(msg, '', opts);
        else toastr.info(msg, '', opts);
    } else {
        alert(msg);
    }
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
        if (announce) npsNotify('warning', npsFormMessage('required') + missing.join('、'));
        first.focus();
        return false;
    }
    return true;
}

$(function () {
    npsDecorateForms('');
    $('[required]').on('input change blur', function () {
        if (this.value && this.value.trim() !== '') npsSetFieldValidity(this, true);
    });
});

function submitform(action, url, postdata) {
    postsubmit = false;
    switch (action) {
        case 'start':
        case 'stop':
        case 'delete':
		case 'copy':
            var langobj = languages['content']['confirm'][action];
            action = (langobj[languages['current']] || langobj[languages['default']] || 'Are you sure you want to ' + action + ' it?');
            if (! confirm(action)) return;
            postsubmit = true;
        case 'add':
        case 'edit':
            var form = document.querySelector('form.form-horizontal');
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
							window.location.href= document.referrer
						}
                    }
                }
            });
			return;
		case 'global':
			var formG = document.querySelector('form.form-horizontal');
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
				}
			});
    }
}

function changeunit(limit) {
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

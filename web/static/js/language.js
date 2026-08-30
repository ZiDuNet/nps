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
    $('[required]').each(function () {
        var label = this.closest('.form-group, .form-field');
        label = label ? label.querySelector('label') : null;
        if (label && !label.querySelector('.required-mark')) {
            var mark = document.createElement('span');
            mark.className = 'required-mark';
            mark.setAttribute('aria-hidden', 'true');
            mark.textContent = '*';
            label.appendChild(mark);
        }
    }).on('input change blur', function () {
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

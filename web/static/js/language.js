(function ($) {

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
			url: window.nps.web_base_url + '/static/page/languages.xml?v=202512051',
			dataType: 'xml',
			success: function (xml) {
				languages['content'] = xml2json($(xml).children())['content'];
				languages['menu'] = languages['content']['languages'];
				languages['default'] = languages['content']['default'];
				languages['navigator'] = (getCookie ('lang') || navigator.language || navigator.browserLanguage);
				for(var key in languages['menu']){
					$('#languagemenu').next().append('<li lang="' + key + '"><a><img src="' + window.nps.web_base_url + '/static/img/flag/' + key + '.png"> ' + languages['menu'][key] +'</a></li>');
					if ( key == languages['navigator'] ) languages['current'] = key;
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
			if (languages['current'] != getCookie('lang')) setCookie('lang', languages['current']);
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
	$('body').on('click','li[lang]',function(){
		$('#languagemenu').attr('lang',$(this).attr('lang'));
		$('body').setLang ('');
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
            // Check required fields before submitting
            var form = document.querySelector('form.form-horizontal');
            if (form) {
                var missing = [];
                var reqs = form.querySelectorAll('input[required], textarea[required], select[required]');
                for (var i = 0; i < reqs.length; i++) {
                    var el = reqs[i];
                    if (el.type === 'checkbox' || el.type === 'radio') {
                        // skip (not used as a required field here)
                        continue;
                    }
                    if (!el.value || el.value.trim() === '') {
                        var name = el.getAttribute('name') || '';
                        // Try to find a human label
                        var lbl = form.querySelector('label[for="' + el.id + '"]');
                        if (!lbl) {
                            var prev = el.closest('.form-group, .form-field');
                            if (prev) lbl = prev.querySelector('label');
                        }
                        var labelText = lbl ? lbl.textContent.replace(/[*\s:：]+$/, '').trim() : (name || '该字段');
                        missing.push(labelText);
                    }
                }
                if (missing.length) {
                    npsNotify('warning', '请填写必填字段：' + missing.join('、'));
                    // Focus the first missing field
                    for (var k = 0; k < reqs.length; k++) {
                        if (reqs[k].type !== 'checkbox' && reqs[k].type !== 'radio'
                            && (!reqs[k].value || reqs[k].value.trim() === '')) {
                            reqs[k].focus();
                            break;
                        }
                    }
                    return;
                }
            }
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
			// Check required fields before submitting
			var formG = document.querySelector('form.form-horizontal');
			if (formG) {
				var missingG = [];
				var reqsG = formG.querySelectorAll('input[required], textarea[required], select[required]');
				for (var j = 0; j < reqsG.length; j++) {
					var eg = reqsG[j];
					if (eg.type === 'checkbox' || eg.type === 'radio') continue;
					if (!eg.value || eg.value.trim() === '') {
						missingG.push(eg.getAttribute('name') || '该字段');
					}
				}
				if (missingG.length) {
					npsNotify('warning', '请填写必填字段：' + missingG.join('、'));
					for (var m = 0; m < reqsG.length; m++) {
						if (reqsG[m].type !== 'checkbox' && reqsG[m].type !== 'radio'
							&& (!reqsG[m].value || reqsG[m].value.trim() === '')) {
							reqsG[m].focus();
							break;
						}
					}
					return;
				}
			}
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
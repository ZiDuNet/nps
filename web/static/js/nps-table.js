/*
 * Lightweight remote data table for the NPS console.
 *
 * The console keeps the server-side table contract used by the Go handlers
 * (offset, limit, search, sort and order), while rendering the table with
 * native HTML and ZUI-compatible primitives. This keeps the data workflow
 * small and avoids coupling the UI to a second component framework.
 */
(function ($, window, document) {
    'use strict';

    if (!$) return;

    var defaults = {
        method: 'get',
        url: window.location.href,
        contentType: 'application/x-www-form-urlencoded',
        queryParams: function (params) { return params; },
        pageNumber: 1,
        pageSize: 10,
        pageList: [10, 20, 50],
        pagination: true,
        search: false,
        showHeader: true,
        showColumns: false,
        showRefresh: false,
        detailView: false,
        cardView: false,
        uniqueId: 'Id',
        columns: [],
        formatLoadingMessage: function () { return '<div class="table-loading-state" role="status">正在加载记录...</div>'; },
        formatNoMatches: function () { return '<div class="table-empty-state" role="status">没有可显示的记录</div>'; }
    };

    function escapeHtml(value) {
        if (typeof window.npsEscapeHtml === 'function') return window.npsEscapeHtml(value == null ? '' : String(value));
        return String(value == null ? '' : value).replace(/[&<>"']/g, function (character) {
            return {'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[character];
        });
    }

    function getPath(object, path) {
        if (!path) return object;
        return String(path).split('.').reduce(function (value, key) {
            return value == null ? undefined : value[key];
        }, object);
    }

    function plainText(value) {
        var holder = document.createElement('div');
        holder.innerHTML = value == null ? '' : String(value);
        return holder.textContent || holder.innerText || '';
    }

    function columnTitle(column) {
        var raw = column && (column.title || column.field || '');
        var title = $.trim(plainText(raw));
        if (title) return title;
        var holder = document.createElement('div');
        holder.innerHTML = raw == null ? '' : String(raw);
        var marker = holder.querySelector('[langtag], [data-i18n-zh]');
        if (marker) {
            var languageKey = marker.getAttribute('langtag');
            var language = typeof languages !== 'undefined' ? languages : null;
            if (languageKey && language && language.content) {
                var translated = language.content[languageKey.toLowerCase()];
                if ($.isArray(translated)) translated = translated[0];
                if (translated && typeof translated === 'object') {
                    translated = translated[language.current] || translated[language.default];
                }
                if (translated) return String(translated);
            }
            var zh = marker.getAttribute('data-i18n-zh');
            var en = marker.getAttribute('data-i18n-en');
            if (language && language.current === 'en-US' && en) return en;
            if (zh) return zh;
            if (languageKey) return languageKey;
        }
        return String(column && column.field || '');
    }

    function callback(instance, name) {
        var fn = instance.options[name];
        if (typeof fn !== 'function') return;
        return fn.apply(instance, Array.prototype.slice.call(arguments, 2));
    }

    function localText(chinese, english) {
        return typeof window.npsIsEnglish === 'function' && window.npsIsEnglish() ? english : chinese;
    }

    function NpsTable(element, options) {
        this.element = element;
        this.$el = $(element);
        this.options = $.extend(true, {}, defaults, options || {});
        this.options.columns = (this.options.columns || []).map(function (column) {
            return $.extend({}, column);
        });
        // Smart tables switch to labelled cards on narrow screens so dense
        // operational data remains readable without introducing page scroll.
        if (this.options.smartDisplay && window.matchMedia && window.matchMedia('(max-width: 991px)').matches) {
            this.options.cardView = true;
        }
        this.pageNumber = Number(this.options.pageNumber) > 0 ? Number(this.options.pageNumber) : 1;
        this.pageSize = Number(this.options.pageSize) > 0 ? Number(this.options.pageSize) : 10;
        this.searchText = '';
        this.sortField = '';
        this.sortOrder = '';
        this.rows = [];
        this.total = 0;
        this.loading = false;
        this._expanded = {};
        this._searchTimer = null;
        this._requestSerial = 0;
        this.init();
    }

    NpsTable.prototype.init = function () {
        var self = this;
        this.$el.addClass('table nps-table__grid').attr('role', 'grid');
        this.$root = this.$el.closest('.nps-table');
        if (!this.$root.length) {
            this.$el.wrap('<div class="nps-table" role="region"></div>');
            this.$root = this.$el.parent();
        }
        this.$root.attr('aria-live', 'polite');
        this.$el.empty();

        var toolbarSelector = this.options.toolbar;
        this.$toolbar = $('<div class="nps-table__toolbar"></div>');
        this.$bars = $('<div class="nps-table__bars"></div>');
        this.$columns = $('<div class="nps-table__columns"></div>');
        this.$search = $('<div class="nps-table__search"></div>');
        this.$toolbar.append(this.$bars, this.$columns, this.$search);
        this.$root.prepend(this.$toolbar);

        if (toolbarSelector) {
            var $source = $(toolbarSelector);
            if ($source.length) this.$bars.append($source.detach());
        }
        if (this.options.showRefresh) {
            this.$bars.append('<button type="button" class="btn btn-outline nps-table__refresh" title="'
                + localText('刷新', 'Refresh') + '" aria-label="' + localText('刷新', 'Refresh') + '"><i class="fa fa-sync-alt" aria-hidden="true"></i></button>');
        }
        if (this.options.showColumns) this.renderColumnsMenu();
        if (this.options.search) {
            this.$search.append('<label class="nps-table__search-field"><span class="sr-only">'
                + localText('搜索', 'Search') + '</span><i class="fa fa-search" aria-hidden="true"></i><input type="search" class="form-control nps-table__search-input" placeholder="'
                + localText('搜索', 'Search') + '" autocomplete="off"></label>');
        }

        this.$container = $('<div class="nps-table__container"></div>');
        this.$body = $('<div class="nps-table__body"></div>');
        this.$headerTable = this.$el;
        this.$bodyTable = this.$el;
        this.$el.wrap(this.$body);
        this.$body = this.$el.parent();
        this.$container.append(this.$body);
        this.$toolbar.after(this.$container);
        this.$pagination = $('<div class="nps-table__pagination"></div>');
        this.$container.after(this.$pagination);

        this.renderHeader();
        this.bindEvents();
        this.$el.data('nps.table', this);
        this.load();
        return this;
    };

    NpsTable.prototype.bindEvents = function () {
        var self = this;
        this.$root.off('.npsTable').on('click.npsTable', '.nps-table__refresh', function () {
            self.refresh();
        }).on('click.npsTable', '.nps-table__columns-toggle', function (event) {
            event.preventDefault();
            var isOpen = !self.$columns.hasClass('is-open');
            self.$columns.toggleClass('is-open', isOpen);
            $(this).attr('aria-expanded', String(isOpen));
        }).on('click.npsTable', function (event) {
            if (!$(event.target).closest('.nps-table__columns').length) {
                self.$columns.removeClass('is-open').find('.nps-table__columns-toggle').attr('aria-expanded', 'false');
            }
        }).on('change.npsTable', '.nps-table__column-toggle', function () {
            var index = Number($(this).attr('data-column-index'));
            if (!self.options.columns[index]) return;
            self.options.columns[index].visible = this.checked;
            self.renderHeader();
            self.renderBody();
            self.renderColumnsMenu();
        }).on('input.npsTable', '.nps-table__search-input', function () {
            var value = this.value;
            window.clearTimeout(self._searchTimer);
            self._searchTimer = window.setTimeout(function () {
                self.searchText = $.trim(value);
                self.pageNumber = 1;
                self.load();
            }, 220);
        }).on('click.npsTable', 'th[data-sort-field]', function () {
            var field = $(this).attr('data-sort-field');
            if (self.sortField === field) self.sortOrder = self.sortOrder === 'asc' ? 'desc' : 'asc';
            else { self.sortField = field; self.sortOrder = 'asc'; }
            self.pageNumber = 1;
            self.renderHeader();
            self.load();
        }).on('change.npsTable', '.nps-table__select-all', function () {
            self.$body.find('.nps-table__row-select').prop('checked', this.checked).trigger('change');
        }).on('change.npsTable', '.nps-table__row-select', function () {
            var $items = self.$body.find('.nps-table__row-select');
            var checked = $items.filter(':checked').length;
            var selectAll = self.$headerTable.find('.nps-table__select-all')[0];
            if (selectAll) {
                selectAll.checked = checked > 0 && checked === $items.length;
                selectAll.indeterminate = checked > 0 && checked < $items.length;
            }
            var $row = $(this).closest('tr');
            $row.toggleClass('is-selected', this.checked);
        }).on('click.npsTable', '.nps-table__detail-toggle', function (event) {
            event.preventDefault();
            self.toggleDetail(Number($(this).closest('tr').attr('data-index')));
        }).on('keydown.npsTable', '.nps-table__detail-toggle', function (event) {
            if (event.key === 'Enter' || event.key === ' ' || event.key === 'Spacebar') {
                event.preventDefault();
                $(this).trigger('click');
            }
        }).on('click.npsTable', '.nps-table__page-button', function () {
            var page = Number($(this).attr('data-page'));
            if (!page || page === self.pageNumber || $(this).prop('disabled')) return;
            self.pageNumber = page;
            self.load();
        }).on('change.npsTable', '.nps-table__page-size', function () {
            var size = Number(this.value);
            if (!size) return;
            self.pageSize = size;
            self.pageNumber = 1;
            self.load();
        });
    };

    NpsTable.prototype.visibleColumns = function () {
        return this.options.columns.filter(function (column) { return column.visible !== false; });
    };

    NpsTable.prototype.renderColumnsMenu = function () {
        var self = this;
        if (!this.options.showColumns) return;
        this.$columns.empty();
        var button = $('<button type="button" class="btn btn-outline nps-table__columns-toggle" aria-haspopup="true" aria-expanded="false"><i class="fa fa-columns" aria-hidden="true"></i><span>'
            + localText('列', 'Columns') + '</span><i class="fa fa-chevron-down nps-table__columns-caret" aria-hidden="true"></i></button>');
        var menu = $('<div class="nps-table__columns-menu" role="menu"></div>');
        this.options.columns.forEach(function (column, index) {
            // Selection columns are structural and should not appear as an
            // empty toggle in the user-facing column picker.
            if (column.checkbox) return;
            var title = columnTitle(column);
            var id = 'nps-column-' + Math.random().toString(36).slice(2);
            var $label = $('<label class="nps-table__column-option" role="menuitemcheckbox"></label>');
            var $input = $('<input type="checkbox" class="nps-table__column-toggle">').attr({
                id: id,
                'data-column-index': index
            }).prop('checked', column.visible !== false);
            $label.append($input, $('<span></span>').text(title));
            menu.append($label);
        });
        this.$columns.append(button, menu);
    };

    NpsTable.prototype.renderHeader = function () {
        var self = this;
        var columns = this.visibleColumns();
        var $head = $('<thead><tr></tr></thead>');
        var $row = $head.find('tr');
        if (this.options.detailView) $row.append('<th class="nps-table__detail-cell" aria-hidden="true"></th>');
        columns.forEach(function (column) {
            var classes = ['nps-table__header-cell'];
            if (column.checkbox) classes.push('nps-checkbox');
            if (column.class) classes.push(column.class);
            var $cell = $('<th></th>').addClass(classes.join(' '));
            if (column.width) $cell.css('width', column.width);
            if (column.sortable && column.field) $cell.attr({'data-sort-field': column.field, 'aria-sort': self.sortField === column.field ? (self.sortOrder === 'desc' ? 'descending' : 'ascending') : 'none'});
            var $inner = $('<div class="nps-table__header-inner"></div>');
            if (column.halign) $inner.css('justify-content', column.halign === 'right' ? 'flex-end' : column.halign);
            if (column.checkbox) {
                $inner.append('<label class="nps-checkbox__label"><input type="checkbox" class="nps-table__select-all" aria-label="'
                    + localText('选择当前页全部记录', 'Select all records on this page') + '"></label>');
            } else {
                $inner.html(column.title || column.field || '');
                if (column.sortable) $inner.append('<span class="nps-table__sort" aria-hidden="true"></span>');
            }
            $cell.append($inner);
            if (column.halign) $cell.css('text-align', column.halign);
            $row.append($cell);
        });
        this.$headerTable.children('thead').remove();
        if (this.options.showHeader !== false) this.$headerTable.prepend($head);
    };

    NpsTable.prototype.renderBody = function () {
        var self = this;
        var columns = this.visibleColumns();
        var $body = $('<tbody></tbody>');
        var colspan = columns.length + (this.options.detailView ? 1 : 0);
        this.$root.toggleClass('is-loading', this.loading).attr('data-card-view', this.options.cardView ? 'true' : 'false');
        if (this.loading) {
            $body.append($('<tr class="nps-table__loading-row"><td colspan="' + colspan + '"></td></tr>').find('td').html(callback(this, 'formatLoadingMessage') || '').end());
        } else if (!this.rows.length) {
            $body.append($('<tr class="no-records-found nps-table__empty-row"><td colspan="' + colspan + '"></td></tr>').find('td').html(callback(this, 'formatNoMatches') || '').end());
        } else {
            this.rows.forEach(function (row, index) {
                var rowKey = getPath(row, self.options.uniqueId) == null ? index : getPath(row, self.options.uniqueId);
                var $row = $('<tr class="nps-table__row"></tr>').attr({'data-index': index, 'data-row-key': rowKey});
                if (self.options.detailView) {
                    var expanded = !!self._expanded[index];
                    $row.append('<td class="nps-table__detail-cell"><a href="#" class="detail-icon nps-table__detail-toggle" role="button" tabindex="0" aria-expanded="'
                        + (expanded ? 'true' : 'false') + '" aria-label="' + (expanded ? localText('折叠详情', 'Collapse details') : localText('展开详情', 'Expand details')) + '"><i class="fa fa-chevron-'
                        + (expanded ? 'down' : 'right') + '" aria-hidden="true"></i></a></td>');
                }
                columns.forEach(function (column) {
                    var classes = ['nps-table__cell'];
                    if (column.checkbox) classes.push('nps-checkbox');
                    if (column.class) classes.push(column.class);
                    var $cell = $('<td></td>').addClass(classes.join(' '));
                    if (column.align) $cell.css('text-align', column.align);
                    if (self.options.cardView) $cell.attr('data-label', plainText(column.title || column.field || ''));
                    if (column.checkbox) {
                        $cell.append('<label class="nps-checkbox__label"><input type="checkbox" class="nps-table__row-select" value="' + escapeHtml(rowKey) + '" data-index="' + index + '" aria-label="'
                            + localText('选择此记录', 'Select this record') + '"></label>');
                    } else {
                        var value = getPath(row, column.field);
                        var rendered;
                        try {
                            rendered = typeof column.formatter === 'function' ? column.formatter(value, row, index) : escapeHtml(value == null ? '' : value);
                        } catch (error) {
                            rendered = escapeHtml(value == null ? '' : value);
                        }
                        if (column.escape && typeof column.formatter !== 'function') rendered = escapeHtml(value == null ? '' : value);
                        $cell.html(rendered == null ? '' : rendered);
                    }
                    $row.append($cell);
                });
                $body.append($row);
                if (self._expanded[index]) self.appendDetail($body, index, row);
            });
        }
        this.$el.children('tbody').remove();
        this.$el.append($body);
        callback(this, 'onPostBody', this.rows);
        this.syncCardLabels();
    };

    NpsTable.prototype.syncCardLabels = function () {
        if (!this.options.cardView) return;
        var self = this;
        var columns = this.visibleColumns();
        var $headers = this.$el.find('thead th');
        if (this.options.detailView) $headers = $headers.slice(1);

        this.$el.find('tbody tr.nps-table__row').each(function () {
            var columnIndex = 0;
            $(this).children('td').each(function (cellIndex) {
                var $cell = $(this);
                if (self.options.detailView && cellIndex === 0) return;
                var column = columns[columnIndex++];
                if (!column || column.checkbox) {
                    $cell.attr('data-label', '');
                    return;
                }
                // onPostBody may translate title markup synchronously. Read the
                // rendered header so responsive cards keep those translated labels.
                var title = $.trim($headers.eq(columnIndex - 1).find('.nps-table__header-inner').text())
                    || plainText(column.title || column.field || '')
                    || column.field
                    || '';
                $cell.attr('data-label', title);
            });
        });
    };

    NpsTable.prototype.appendDetail = function ($body, index, row) {
        var colspan = this.visibleColumns().length + 1;
        var $detail = $('<tr class="detail-view nps-detail-view"><td colspan="' + colspan + '"></td></tr>');
        var $cell = $detail.find('td');
        var html = typeof this.options.detailFormatter === 'function' ? this.options.detailFormatter(index, row, $cell) : '';
        $cell.html(html == null ? '' : html);
        $body.append($detail);
    };

    NpsTable.prototype.toggleDetail = function (index) {
        if (index < 0 || index >= this.rows.length) return;
        var row = this.rows[index];
        var expanded = !!this._expanded[index];
        if (expanded) {
            delete this._expanded[index];
            this.renderBody();
            callback(this, 'onCollapseRow', index, row);
        } else {
            this._expanded[index] = true;
            this.renderBody();
            callback(this, 'onExpandRow', index, row, this.$el.find('tr.nps-detail-view').last().find('td'));
        }
        if (typeof window.npsDecorateDetailControls === 'function') window.npsDecorateDetailControls(this.$root);
    };

    NpsTable.prototype.load = function () {
        var self = this;
        var serial = ++this._requestSerial;
        var params = {
            offset: (this.pageNumber - 1) * this.pageSize,
            limit: this.pageSize,
            search: this.searchText,
            sort: this.sortField,
            order: this.sortOrder
        };
        var query = typeof this.options.queryParams === 'function' ? this.options.queryParams(params) : params;
        if (query == null) query = params;
        this.loading = true;
        this.renderBody();
        $.ajax({
            type: this.options.method || 'get',
            url: this.options.url,
            data: query,
            dataType: 'json',
            contentType: this.options.contentType
        }).done(function (data) {
            if (serial !== self._requestSerial) return;
            data = data || {};
            self.rows = Array.isArray(data.rows) ? data.rows : [];
            self.total = Number(data.total);
            if (!isFinite(self.total) || self.total < 0) self.total = self.rows.length;
            var maxPage = Math.max(1, Math.ceil(self.total / self.pageSize));
            if (self.pageNumber > maxPage) self.pageNumber = maxPage;
            self.loading = false;
            self._expanded = {};
            self.renderBody();
            self.renderPagination();
            callback(self, 'onLoadSuccess', data);
            if (typeof window.npsDecorateDetailControls === 'function') window.npsDecorateDetailControls(self.$root);
        }).fail(function (xhr) {
            if (serial !== self._requestSerial) return;
            self.rows = [];
            self.total = 0;
            self.loading = false;
            self.renderBody();
            self.renderPagination();
            callback(self, 'onLoadError', xhr);
        });
    };

    NpsTable.prototype.renderPagination = function () {
        var self = this;
        this.$pagination.empty();
        if (!this.options.pagination) return;
        var pageTotal = Math.max(1, Math.ceil(this.total / this.pageSize));
        var first = this.total ? ((this.pageNumber - 1) * this.pageSize + 1) : 0;
        var last = Math.min(this.total, this.pageNumber * this.pageSize);
        var $detail = $('<div class="nps-table__pagination-detail"></div>');
        $detail.append('<span>' + localText('显示第 ' + first + ' 到 ' + last + ' 条记录，共 ' + this.total + ' 条', 'Showing ' + first + ' to ' + last + ' of ' + this.total + ' records') + '</span>');
        var $size = $('<label class="nps-table__page-size-wrap"><span>' + localText('每页', 'Rows') + '</span><select class="form-control nps-table__page-size"></select></label>');
        (this.options.pageList || [10, 20, 50]).forEach(function (size) {
            $size.find('select').append($('<option></option>').attr('value', size).prop('selected', Number(size) === self.pageSize).text(size));
        });
        $detail.append($size);
        var $nav = $('<nav class="nps-table__page-nav" aria-label="' + localText('分页', 'Pagination') + '"></nav>');
        var $pages = $('<div class="nps-table__pages"></div>');
        function pageButton(label, page, disabled, current) {
            return $('<button type="button" class="nps-table__page-button' + (current ? ' is-active' : '') + '"></button>').attr({'data-page': page, 'aria-label': label, 'aria-current': current ? 'page' : null, disabled: disabled}).text(label);
        }
        $pages.append(pageButton(localText('上一页', 'Previous'), this.pageNumber - 1, this.pageNumber <= 1));
        var start = Math.max(1, this.pageNumber - 2);
        var end = Math.min(pageTotal, start + 4);
        start = Math.max(1, end - 4);
        for (var page = start; page <= end; page++) $pages.append(pageButton(String(page), page, false, page === this.pageNumber));
        $pages.append(pageButton(localText('下一页', 'Next'), this.pageNumber + 1, this.pageNumber >= pageTotal));
        $nav.append($pages);
        this.$pagination.append($detail, $nav);
    };

    NpsTable.prototype.refresh = function () {
        this.load();
    };

    NpsTable.prototype.refreshOptions = function (options) {
        options = options || {};
        this.options = $.extend(true, this.options, options);
        if (options.columns) this.options.columns = options.columns.map(function (column) { return $.extend({}, column); });
        this.renderHeader();
        if (this.options.showColumns) this.renderColumnsMenu();
        this.load();
    };

    NpsTable.prototype.getData = function () { return this.rows.slice(); };

    NpsTable.prototype.getSelections = function () {
        var self = this;
        var selected = [];
        this.$body.find('.nps-table__row-select:checked').each(function () {
            var index = Number($(this).attr('data-index'));
            if (self.rows[index]) selected.push(self.rows[index]);
        });
        return selected;
    };

    NpsTable.prototype.destroy = function () {
        window.clearTimeout(this._searchTimer);
        this.$root.off('.npsTable');
        this.$el.removeData('nps.table').removeData('npsTable');
        this.$el.unwrap('.nps-table__body').unwrap('.nps-table__container').unwrap('.nps-table');
    };

    $.fn.npsTable = function (option) {
        var args = Array.prototype.slice.call(arguments, 1);
        var returnValue;
        this.each(function () {
            var $table = $(this);
            var instance = $table.data('nps.table');
            if (!instance && typeof option !== 'string') {
                instance = new NpsTable(this, option);
                $table.data('nps.table', instance).data('npsTable', instance);
            } else if (!instance) {
                return;
            }
            if (typeof option === 'string' && typeof instance[option] === 'function') {
                var result = instance[option].apply(instance, args);
                if (returnValue === undefined && result !== undefined) returnValue = result;
            }
        });
        return returnValue === undefined ? this : returnValue;
    };

    $.fn.npsTable.Constructor = NpsTable;

    window.batchDelete = window.batchDelete || function (url) {
        var ids = [];
        $('#table').closest('.nps-table').find('.nps-table__row-select:checked').each(function () {
            var id = Number($(this).val());
            if (isFinite(id) && id > 0) ids.push(Math.floor(id));
        });
        ids = ids.filter(function (id, index) { return ids.indexOf(id) === index; });
        if (!ids.length) {
            if (typeof window.npsNotify === 'function') window.npsNotify('warning', localText('请先选择要删除的记录。', 'Select at least one record first.'));
            return false;
        }
        if (!window.confirm(localText('确定要删除选中的 ' + ids.length + ' 条记录吗？', 'Delete ' + ids.length + ' selected record(s)?'))) return false;
        var removeNext = function (index) {
            if (index >= ids.length) {
                $('#table').npsTable('refresh');
                if (typeof window.npsNotify === 'function') window.npsNotify('success', localText('删除成功。', 'Deleted successfully.'));
                return;
            }
            $.ajax({type: 'POST', url: url, data: {id: ids[index]}, dataType: 'json'}).done(function (result) {
                if (!result || !result.status) {
                    if (typeof window.npsNotify === 'function') window.npsNotify('error', (result && result.msg) || localText('删除失败。', 'Delete failed.'));
                    return;
                }
                removeNext(index + 1);
            }).fail(function () {
                if (typeof window.npsNotify === 'function') window.npsNotify('error', localText('删除请求失败。', 'Delete request failed.'));
            });
        };
        removeNext(0);
        return false;
    };
})(window.jQuery, window, document);

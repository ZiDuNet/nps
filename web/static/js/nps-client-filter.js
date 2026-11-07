/*
 * Accessible client-filter decoration.
 *
 * Native <option> elements cannot contain reliable status badges across
 * browsers. Keep the original <select> as the source of truth and render a
 * synchronized button + listbox beside it for richer labels and keyboard
 * interaction.
 */
(function (window, document) {
    'use strict';

    var sequence = 0;

    function isEnglish() {
        if (typeof window.npsIsEnglish === 'function') return window.npsIsEnglish();
        return String(document.documentElement.lang || '').toLowerCase().indexOf('en') === 0;
    }

    function statusText(online) {
        if (online) return isEnglish() ? 'Online' : '在线';
        return isEnglish() ? 'Offline' : '离线';
    }

    function optionOnline(option) {
        if (!option || !option.hasAttribute('data-client-online')) return null;
        return String(option.getAttribute('data-client-online')).toLowerCase() === 'true';
    }

    function optionLabel(option) {
        return String(option && option.textContent || '').replace(/\s+/g, ' ').trim();
    }

    function selectedOption(select) {
        return select.options[select.selectedIndex] || select.options[0] || null;
    }

    function filterLabel(select) {
        var labelledBy = String(select.getAttribute('aria-labelledby') || '').trim();
        if (labelledBy) {
            var labels = labelledBy.split(/\s+/).map(function (id) {
                var node = document.getElementById(id);
                return node ? String(node.textContent || '').trim() : '';
            }).filter(Boolean);
            if (labels.length) return labels.join(' ');
        }
        var container = select.closest('.nps-table-filter');
        var label = container && container.querySelector('.nps-table-filter__label');
        if (label && String(label.textContent || '').trim()) return String(label.textContent).trim();
        return String(select.getAttribute('aria-label') || '').trim();
    }

    function makeStatus(online) {
        var badge = document.createElement('span');
        badge.className = 'nps-client-filter__status ' + (online ? 'is-online' : 'is-offline');
        badge.textContent = statusText(online);
        return badge;
    }

    function createOption(instance, option, index) {
        var online = optionOnline(option);
        var value = String(option.value);
        var row = document.createElement('div');
        row.className = 'nps-client-filter__option';
        row.id = instance.id + '-option-' + index;
        row.setAttribute('role', 'option');
        row.setAttribute('tabindex', '-1');
        row.setAttribute('data-value', value);
        row.setAttribute('aria-selected', value === String(instance.select.value) ? 'true' : 'false');

        var text = document.createElement('span');
        text.className = 'nps-client-filter__option-label';
        text.textContent = optionLabel(option);
        row.appendChild(text);
        if (online !== null) row.appendChild(makeStatus(online));

        row.addEventListener('click', function () {
            instance.choose(value);
        });
        row.addEventListener('keydown', function (event) {
            instance.optionKeydown(event);
        });
        return row;
    }

    function ClientFilter(select) {
        this.select = select;
        this.id = 'nps-client-filter-' + (++sequence);
        this.options = [];
        this.activeIndex = -1;
        this.open = false;
        this.build();
        this.bind();
        this.refresh();
    }

    ClientFilter.prototype.build = function () {
        var wrapper = document.createElement('div');
        wrapper.className = 'nps-client-filter';
        wrapper.setAttribute('data-nps-client-filter', 'true');

        var trigger = document.createElement('button');
        trigger.type = 'button';
        trigger.className = 'nps-client-filter__trigger';
        trigger.setAttribute('aria-haspopup', 'listbox');
        trigger.setAttribute('aria-expanded', 'false');
        trigger.setAttribute('aria-controls', this.id + '-listbox');

        var label = document.createElement('span');
        label.className = 'nps-client-filter__label';
        var status = document.createElement('span');
        status.className = 'nps-client-filter__status-slot';
        var caret = document.createElement('span');
        caret.className = 'nps-client-filter__caret';
        caret.setAttribute('aria-hidden', 'true');
        trigger.appendChild(label);
        trigger.appendChild(status);
        trigger.appendChild(caret);

        var listbox = document.createElement('div');
        listbox.id = this.id + '-listbox';
        listbox.className = 'nps-client-filter__menu';
        listbox.setAttribute('role', 'listbox');
        listbox.hidden = true;

        wrapper.appendChild(trigger);
        wrapper.appendChild(listbox);
        this.select.parentNode.insertBefore(wrapper, this.select.nextSibling);
        this.select.classList.add('nps-client-filter__native');
        this.select.setAttribute('tabindex', '-1');
        this.select.setAttribute('aria-hidden', 'true');

        this.wrapper = wrapper;
        this.trigger = trigger;
        this.label = label;
        this.statusSlot = status;
        this.listbox = listbox;
    };

    ClientFilter.prototype.bind = function () {
        var self = this;
        this.reposition = function (event) {
            if (!self.open) return;
            if (event && event.target === self.listbox) return;
            self.positionMenu();
        };
        this.trigger.addEventListener('click', function () {
            if (self.select.disabled) return;
            self.toggle();
        });
        this.trigger.addEventListener('keydown', function (event) {
            if (self.select.disabled) return;
            if (event.key === 'ArrowDown' || event.key === 'ArrowUp' || event.key === 'Home' || event.key === 'End') {
                event.preventDefault();
                self.openMenu(event.key);
            } else if (event.key === 'Escape' && self.open) {
                event.preventDefault();
                self.close(true);
            }
        });
        this.listbox.addEventListener('keydown', function (event) {
            if (event.key === 'Tab') {
                self.close(false);
            }
        });
        this.select.addEventListener('change', function () {
            self.refresh();
        });
        document.addEventListener('click', function (event) {
            if (self.open && !self.wrapper.contains(event.target)) self.close(false);
        });
        window.addEventListener('resize', this.reposition, {passive: true});
        window.addEventListener('scroll', this.reposition, true);
        if (window.visualViewport) {
            window.visualViewport.addEventListener('resize', this.reposition, {passive: true});
            window.visualViewport.addEventListener('scroll', this.reposition, {passive: true});
        }
    };

    ClientFilter.prototype.refresh = function () {
        var selected = selectedOption(this.select);
        var online = optionOnline(selected);
        var label = optionLabel(selected);
        var fieldLabel = filterLabel(this.select);
        var accessibleLabel = fieldLabel && label ? fieldLabel + ': ' + label : (fieldLabel || label);
        if (online !== null) {
            accessibleLabel += ', ' + statusText(online);
        }

        this.label.textContent = label;
        this.label.title = label;
        this.statusSlot.textContent = '';
        if (online !== null) this.statusSlot.appendChild(makeStatus(online));

        this.trigger.setAttribute('aria-label', accessibleLabel);
        this.trigger.disabled = !!this.select.disabled;
        this.wrapper.classList.toggle('is-disabled', !!this.select.disabled);
        this.listbox.setAttribute('aria-label', fieldLabel || label);

        var selectedValue = String(this.select.value);
        this.options = [];
        this.listbox.textContent = '';
        var fragment = document.createDocumentFragment();
        Array.prototype.forEach.call(this.select.options, function (option, index) {
            var row = createOption(this, option, index);
            this.options.push(row);
            if (String(option.value) === selectedValue) this.activeIndex = index;
            fragment.appendChild(row);
        }, this);
        this.listbox.appendChild(fragment);
        if (this.select.disabled && this.open) this.close(false);
        if (this.open) this.positionMenu();
    };

    ClientFilter.prototype.toggle = function () {
        if (this.open) this.close(true);
        else this.openMenu();
    };

    ClientFilter.prototype.openMenu = function (key) {
        if (!this.options.length) return;
        this.open = true;
        this.listbox.hidden = false;
        this.trigger.setAttribute('aria-expanded', 'true');
        this.wrapper.classList.add('is-open');
        this.positionMenu();

        var selected = this.options[this.activeIndex] || this.options[0];
        if (key === 'Home') selected = this.options[0];
        if (key === 'End') selected = this.options[this.options.length - 1];
        if (key === 'ArrowUp') selected = this.options[Math.max(0, this.activeIndex - 1)] || selected;
        if (key === 'ArrowDown') selected = this.options[Math.min(this.options.length - 1, this.activeIndex + 1)] || selected;
        this.focusOption(selected);
    };

    ClientFilter.prototype.close = function (returnFocus) {
        if (!this.open) return;
        this.open = false;
        this.listbox.hidden = true;
        this.trigger.setAttribute('aria-expanded', 'false');
        this.wrapper.classList.remove('is-open', 'opens-up');
        this.listbox.style.removeProperty('visibility');
        if (returnFocus) this.trigger.focus();
    };

    ClientFilter.prototype.positionMenu = function () {
        if (!this.open || this.listbox.hidden) return;

        var margin = 12;
        var gap = 6;
        var triggerRect = this.trigger.getBoundingClientRect();
        var viewportWidth = window.innerWidth || document.documentElement.clientWidth || 0;
        var viewportHeight = window.innerHeight || document.documentElement.clientHeight || 0;
        if (!viewportWidth || !viewportHeight) return;

        var maxWidth = Math.max(0, Math.min(360, viewportWidth - margin * 2));
        this.listbox.style.visibility = 'hidden';
        this.listbox.style.maxHeight = 'none';
        this.listbox.style.minWidth = Math.round(Math.min(triggerRect.width, maxWidth)) + 'px';
        this.listbox.style.maxWidth = Math.round(maxWidth) + 'px';
        this.listbox.style.left = '0px';
        this.listbox.style.right = 'auto';
        this.listbox.style.top = '0px';

        var naturalHeight = Math.ceil(this.listbox.scrollHeight);
        var availableBelow = Math.max(0, viewportHeight - triggerRect.bottom - gap - margin);
        var availableAbove = Math.max(0, triggerRect.top - gap - margin);
        var openAbove = availableBelow < naturalHeight && availableAbove > availableBelow;
        var availableHeight = openAbove ? availableAbove : availableBelow;
        this.listbox.style.maxHeight = Math.floor(availableHeight) + 'px';

        var menuRect = this.listbox.getBoundingClientRect();
        var menuWidth = Math.min(menuRect.width, maxWidth);
        var menuHeight = Math.min(naturalHeight, Math.ceil(menuRect.height), availableHeight);
        var preferredLeft = triggerRect.right - menuWidth;
        var left = Math.max(margin, Math.min(preferredLeft, viewportWidth - margin - menuWidth));
        var preferredTop = openAbove
            ? triggerRect.top - gap - menuHeight
            : triggerRect.bottom + gap;
        var top = Math.max(margin, Math.min(preferredTop, viewportHeight - margin - menuHeight));

        this.listbox.style.left = Math.round(left) + 'px';
        this.listbox.style.top = Math.round(top) + 'px';
        this.listbox.style.visibility = 'visible';
        this.wrapper.classList.toggle('opens-up', openAbove);
    };

    ClientFilter.prototype.focusOption = function (row) {
        if (!row) return;
        this.activeIndex = this.options.indexOf(row);
        row.focus();
        if (typeof row.scrollIntoView === 'function') {
            row.scrollIntoView({block: 'nearest'});
        }
    };

    ClientFilter.prototype.optionKeydown = function (event) {
        var index = this.options.indexOf(event.currentTarget);
        var next = null;
        if (event.key === 'ArrowDown') next = Math.min(this.options.length - 1, index + 1);
        else if (event.key === 'ArrowUp') next = Math.max(0, index - 1);
        else if (event.key === 'Home') next = 0;
        else if (event.key === 'End') next = this.options.length - 1;
        else if (event.key === 'Enter' || event.key === ' ' || event.key === 'Spacebar') {
            event.preventDefault();
            this.choose(String(event.currentTarget.getAttribute('data-value') || ''));
            return;
        } else if (event.key === 'Escape') {
            event.preventDefault();
            this.close(true);
            return;
        } else {
            return;
        }
        event.preventDefault();
        this.focusOption(this.options[next]);
    };

    ClientFilter.prototype.choose = function (value) {
        if (this.select.disabled) return;
        Array.prototype.some.call(this.select.options, function (option) {
            if (String(option.value) !== String(value)) return false;
            this.select.selectedIndex = option.index;
            return true;
        }, this);
        this.select.dispatchEvent(new Event('change', {bubbles: true}));
        this.close(true);
    };

    function enhance(select) {
        if (!select || select.tagName !== 'SELECT') return null;
        if (!select.__npsClientFilter) select.__npsClientFilter = new ClientFilter(select);
        else select.__npsClientFilter.refresh();
        return select.__npsClientFilter;
    }

    window.NpsClientFilter = {
        enhance: enhance,
        refresh: function (select) {
            return enhance(select);
        }
    };
})(window, document);

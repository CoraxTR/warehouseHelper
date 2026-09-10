// nav.js — выдвижной сайдбар навигации warehouseHelper.
// ES5 (старые браузеры склада): без const/let/стрелок/NodeList.forEach.
// Тоггл: кнопка #nav-toggle / #nav-close / клик по scrim / Escape.
// Состояние: nav_open ('1'/'0') и nav_groups (список раскрытых групп).
// Подсветка: точное совпадение data-path с pathname+query, иначе самое
// длинное совпадение по началу пути на границе сегмента ('/' — только точно).
(function () {
    var KEY_OPEN = 'nav_open';
    var KEY_GROUPS = 'nav_groups';

    function byId(id) {
        return document.getElementById(id);
    }

    function store(key, value) {
        try { window.localStorage.setItem(key, value); } catch (e) { /* приватный режим */ }
    }

    function load(key) {
        try { return window.localStorage.getItem(key); } catch (e) { return null; }
    }

    function groupKey(el) {
        return el.getAttribute('data-group');
    }

    function groups() {
        var nav = byId('sidebar');
        if (!nav) { return []; }
        var all = nav.getElementsByTagName('div');
        var out = [];
        for (var i = 0; i < all.length; i++) {
            if ((' ' + all[i].className + ' ').indexOf(' sidebar__group ') >= 0) {
                out.push(all[i]);
            }
        }
        return out;
    }

    function isExpanded(el) {
        return (' ' + el.className + ' ').indexOf(' expanded ') >= 0;
    }

    function setExpanded(el, on) {
        if (on) {
            if (!isExpanded(el)) { el.className = el.className + ' expanded'; }
            return;
        }
        el.className = (' ' + el.className + ' ').replace(' expanded ', ' ');
        el.className = el.className.replace(/^\s+|\s+$/g, '');
    }

    function saveGroups() {
        var all = groups();
        var keys = [];
        for (var i = 0; i < all.length; i++) {
            if (isExpanded(all[i])) { keys.push(groupKey(all[i])); }
        }
        store(KEY_GROUPS, keys.join(','));
    }

    // Активный пункт меню: null, если страницы нет в дереве.
    function activeLink() {
        var nav = byId('sidebar');
        if (!nav) { return null; }
        var cur = window.location.pathname + window.location.search;
        var all = nav.getElementsByTagName('a');
        var best = null;
        var bestLen = -1;
        for (var i = 0; i < all.length; i++) {
            var path = all[i].getAttribute('data-path');
            if (!path) { continue; }
            if (path === cur) { return all[i]; }
            if (path === '/' || cur.indexOf(path) !== 0) { continue; }
            // граница сегмента: /goods не должен ловить /goods-tree
            var next = cur.charAt(path.length);
            if (next !== '/' && next !== '?') { continue; }
            if (path.length > bestLen) { best = all[i]; bestLen = path.length; }
        }
        return best;
    }

    function highlight() {
        var link = activeLink();
        if (!link) { return; }
        link.className = link.className + ' active';
    }

    function isOpen() {
        var nav = byId('sidebar');
        return !!nav && (' ' + nav.className + ' ').indexOf(' open ') >= 0;
    }

    function setOpen(on) {
        var nav = byId('sidebar');
        if (!nav) { return; }
        var scrim = byId('sidebar-scrim');
        var toggle = byId('nav-toggle');
        if (on) {
            if (!isOpen()) { nav.className = nav.className + ' open'; }
            if (scrim) { scrim.className = scrim.className + ' open'; }
        } else {
            nav.className = (' ' + nav.className + ' ').replace(' open ', ' ').replace(/^\s+|\s+$/g, '');
            if (scrim) { scrim.className = (' ' + scrim.className + ' ').replace(' open ', ' ').replace(/^\s+|\s+$/g, ''); }
        }
        if (toggle) { toggle.setAttribute('aria-expanded', on ? 'true' : 'false'); }
        store(KEY_OPEN, on ? '1' : '0');
    }

    // Раскрытие групп: активную ветку держим раскрытой всегда; остальные — по
    // сохранённому состоянию, а без него всё свёрнуто.
    function initGroups() {
        var active = activeLink();
        var saved = load(KEY_GROUPS);
        var keys = saved === null ? null : saved.split(',');
        var all = groups();
        for (var i = 0; i < all.length; i++) {
            var el = all[i];
            if (active && el.contains && el.contains(active)) {
                setExpanded(el, true);
                continue;
            }
            if (keys === null) {
                setExpanded(el, false);
                continue;
            }
            var on = false;
            for (var j = 0; j < keys.length; j++) {
                if (keys[j] === groupKey(el)) { on = true; }
            }
            setExpanded(el, on);
        }
    }

    function bindGroupHeads() {
        var heads = document.getElementsByClassName('sidebar__head-btn');
        for (var i = 0; i < heads.length; i++) {
            heads[i].onclick = function () {
                var g = this.parentNode;
                setExpanded(g, !isExpanded(g));
                saveGroups();
            };
        }
    }

    function init() {
        if (!byId('sidebar')) { return; }

        initGroups();
        highlight();
        bindGroupHeads();

        var toggle = byId('nav-toggle');
        var close = byId('nav-close');
        var scrim = byId('sidebar-scrim');

        if (toggle) {
            toggle.onclick = function () { setOpen(!isOpen()); };
        }
        if (close) {
            close.onclick = function () { setOpen(false); };
        }
        if (scrim) {
            scrim.onclick = function () { setOpen(false); };
        }
        if (document.addEventListener) {
            document.addEventListener('keydown', function (e) {
                var code = e.keyCode || e.which;
                if (code === 27) { setOpen(false); }
            }, false);
        }

        setOpen(load(KEY_OPEN) === '1');
    }

    if (document.addEventListener) {
        document.addEventListener('DOMContentLoaded', init, false);
    } else {
        window.onload = init;
    }
})();

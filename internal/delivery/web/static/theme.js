// theme.js — переключение темы оформления warehouseHelper.
// ES5 (старые браузеры склада): без const/let/стрелок/NodeList.forEach.
//
// Темы: тёмная «Нормальная тема» (база raven-guard.css) и дневная «Любитель
// щурится» (секция html.day там же). Выбор живёт на УСТРОЙСТВЕ — localStorage,
// ключ wh_theme ('day' | 'night'): сервер о теме не знает, у каждого склада и
// телефона своя.
//
// Скрипт подключён в <head> БЕЗ defer: класс day ставится ДО первой отрисовки,
// иначе выбравший дневную увидит вспышку тёмного. Вид ползунка и подпись тоже
// привязаны к классу day в CSS, а не к JS-классам — по той же причине: если
// красить их из JS, подпись на миг покажет чужую тему (ловилось jsdom-тестом
// theme_switch_check.js).
(function () {
    var KEY = 'wh_theme';
    var DAY = 'day';
    var NIGHT_LABEL = 'Нормальная тема';
    var DAY_LABEL = 'Любитель щурится';

    var root = document.documentElement;

    function store(value) {
        try { window.localStorage.setItem(KEY, value); } catch (e) { /* приватный режим */ }
    }

    function load() {
        try { return window.localStorage.getItem(KEY); } catch (e) { return null; }
    }

    function hasClass(el, cls) {
        return (' ' + el.className + ' ').indexOf(' ' + cls + ' ') >= 0;
    }

    function addClass(el, cls) {
        if (!hasClass(el, cls)) {
            el.className = el.className ? el.className + ' ' + cls : cls;
        }
    }

    function removeClass(el, cls) {
        el.className = (' ' + el.className + ' ')
            .replace(' ' + cls + ' ', ' ').replace(/^\s+|\s+$/g, '');
    }

    function isDay() {
        return hasClass(root, DAY);
    }

    // Красим не здесь: цвет, положение кружка и подпись — в CSS по классу day.
    // JS отвечает только за доступность: подпись называет ТЕКУЩУЮ тему
    // (задание владельца 30.09.2026), подсказка — на что переключит клик.
    function paint() {
        var btn = document.getElementById('theme-toggle');
        if (!btn) { return; }
        var day = isDay();
        btn.setAttribute('aria-checked', day ? 'true' : 'false');
        btn.setAttribute('title', day
            ? 'Переключить на «' + NIGHT_LABEL + '»'
            : 'Переключить на «' + DAY_LABEL + '»');
    }

    function setDay(on) {
        if (on) { addClass(root, DAY); } else { removeClass(root, DAY); }
        store(on ? 'day' : 'night');
        paint();
    }

    // 1. Тема — сразу, до отрисовки (скрипт стоит в <head>).
    if (load() === 'day') { addClass(root, DAY); }

    function init() {
        var btn = document.getElementById('theme-toggle');
        if (btn) {
            btn.onclick = function () { setDay(!isDay()); };
        }
        paint();
    }

    // 2. Клик и aria — когда разметка уже разобрана.
    if (document.readyState === 'loading') {
        if (document.addEventListener) {
            document.addEventListener('DOMContentLoaded', init, false);
        } else {
            window.attachEvent('onload', init);
        }
    } else {
        init();
    }
})();

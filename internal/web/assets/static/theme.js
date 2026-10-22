// Runs before the page paints so a saved theme doesn't flash.
try { var t = localStorage.getItem('theme'); if (t === 'light' || t === 'dark') document.documentElement.setAttribute('data-theme', t); } catch (e) {}
try { if (localStorage.getItem('dust') !== 'show') document.documentElement.classList.add('hide-dust'); } catch (e) {}
document.documentElement.classList.add('js-on');

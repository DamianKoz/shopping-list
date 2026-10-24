'use strict';

// Applies the chosen theme, or the system setting if none was chosen. Loaded
// synchronously in <head> so the page never flashes in the wrong colors.
(() => {
  const root = document.documentElement;
  const system = matchMedia('(prefers-color-scheme: dark)');
  let chosen = null;
  try {
    chosen = localStorage.getItem('theme');
  } catch {}

  function apply() {
    const theme = chosen || (system.matches ? 'dark' : 'light');
    root.dataset.theme = theme;
    for (const meta of document.querySelectorAll('meta[name="theme-color"]')) {
      meta.content = theme === 'dark' ? '#1b1a18' : '#faf8f3';
    }
    const button = document.getElementById('theme-toggle');
    if (button) {
      button.textContent = theme === 'dark' ? '☀︎' : '☾︎';
      button.setAttribute('aria-label', theme === 'dark' ? 'Hellen Modus einschalten' : 'Dunklen Modus einschalten');
    }
  }

  system.addEventListener('change', apply);
  document.addEventListener('DOMContentLoaded', () => {
    apply();
    const button = document.getElementById('theme-toggle');
    if (!button) return;
    button.addEventListener('click', () => {
      chosen = root.dataset.theme === 'dark' ? 'light' : 'dark';
      try {
        localStorage.setItem('theme', chosen);
      } catch {}
      apply();
    });
  });
  apply();
})();

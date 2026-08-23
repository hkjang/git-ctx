/**
 * git-ctx Landing Page Interactive Scripts
 * Language switching (KO / EN), Tool Explorer Tabs, Copy Snippets, Accordions
 */

document.addEventListener('DOMContentLoaded', () => {
  initLanguageSwitcher();
  initToolExplorer();
  initFaqAccordion();
  initCopyButtons();
  initMobileNav();
});

/* Language Switcher
 *
 * The site ships two fully authored static pages: index.html (Korean) and
 * index_en.html (English). Switching language therefore navigates between the
 * two files -- it never rewrites text in place, because each page already holds
 * the final copy for its language. The inline detector in each page's <head>
 * performs the first-visit redirect before paint and shares LANG_KEY with this
 * module so the two never disagree.
 */
const LANG_KEY = 'git_ctx_lang';

function writeLangPref(lang) {
  try {
    localStorage.setItem(LANG_KEY, lang);
  } catch (err) {
    /* Preference is best effort; navigation still works without it. */
  }
}

function currentPageLang() {
  return document.documentElement.lang === 'en' ? 'en' : 'ko';
}

function langHref(lang) {
  const file = lang === 'en' ? 'index_en.html' : 'index.html';
  return window.location.pathname.replace(/[^/]*$/, file) +
    window.location.search + window.location.hash;
}

function initLanguageSwitcher() {
  const langKoBtn = document.getElementById('lang-ko');
  const langEnBtn = document.getElementById('lang-en');

  if (!langKoBtn || !langEnBtn) return;

  const current = currentPageLang();

  [['ko', langKoBtn], ['en', langEnBtn]].forEach(([lang, btn]) => {
    btn.classList.toggle('active', lang === current);
    btn.setAttribute('aria-pressed', String(lang === current));
    btn.addEventListener('click', () => {
      writeLangPref(lang);
      if (lang !== current) window.location.href = langHref(lang);
    });
  });
}

/* Tool Explorer Tabs */
function initToolExplorer() {
  const tabBtns = document.querySelectorAll('.tool-tab-btn');
  const tabDetails = document.querySelectorAll('.tool-detail');

  tabBtns.forEach(btn => {
    btn.addEventListener('click', () => {
      const targetId = btn.getAttribute('data-target');

      tabBtns.forEach(b => b.classList.remove('active'));
      tabDetails.forEach(d => d.classList.remove('active'));

      btn.classList.add('active');
      const targetEl = document.getElementById(targetId);
      if (targetEl) {
        targetEl.classList.add('active');
      }
    });
  });
}

/* FAQ Accordion */
function initFaqAccordion() {
  const faqItems = document.querySelectorAll('.faq-item');

  faqItems.forEach(item => {
    const question = item.querySelector('.faq-question');
    if (!question) return;

    question.addEventListener('click', () => {
      const isActive = item.classList.contains('active');

      // Close all items
      faqItems.forEach(i => i.classList.remove('active'));

      // Toggle current item
      if (!isActive) {
        item.classList.add('active');
      }
    });
  });
}

/* Code Copy Buttons */
function initCopyButtons() {
  const copyBtns = document.querySelectorAll('.copy-btn');

  copyBtns.forEach(btn => {
    btn.addEventListener('click', async () => {
      const targetId = btn.getAttribute('data-code');
      const codeEl = document.getElementById(targetId);
      if (!codeEl) return;

      const codeText = codeEl.innerText || codeEl.textContent;

      try {
        await navigator.clipboard.writeText(codeText);
        const originalText = btn.innerHTML;
        btn.innerHTML = `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="#10B981" stroke-width="2"><polyline points="20 6 9 17 4 12"></polyline></svg> Copied!`;
        setTimeout(() => {
          btn.innerHTML = originalText;
        }, 2000);
      } catch (err) {
        console.error('Failed to copy text: ', err);
      }
    });
  });
}

/* Mobile Nav Toggle */
function initMobileNav() {
  const toggleBtn = document.getElementById('mobile-menu-btn');
  const navMenu = document.querySelector('.nav-menu');

  if (!toggleBtn || !navMenu) return;

  // Visibility is owned by CSS (.nav-menu.active inside the max-width:768px
  // media query), so widening back to desktop restores the normal nav on its
  // own -- which inline styles would not.
  const setOpen = (open) => {
    navMenu.classList.toggle('active', open);
    toggleBtn.setAttribute('aria-expanded', String(open));
  };

  setOpen(false);

  toggleBtn.addEventListener('click', () => {
    setOpen(!navMenu.classList.contains('active'));
  });

  // Close after choosing a destination so the anchor target is not hidden
  // behind the open menu.
  navMenu.querySelectorAll('a').forEach((link) => {
    link.addEventListener('click', () => setOpen(false));
  });
}

/**
 * Injects a copy-to-clipboard icon button into every `[data-copy]` on the page.
 * The button is drawn inside the code box (top-right), shows an SVG copy icon
 * with a native "Copy" tooltip on hover, and swaps to a checkmark once copied.
 * On touch screens there is no hover, so the button stays visible; on larger
 * screens it fades in on hover. Idempotent, and no-JS visitors just see the code.
 */
export function initCopyButtons(): void {
  if (typeof document === 'undefined' || !navigator.clipboard) return;

  for (const el of document.querySelectorAll<HTMLElement>('[data-copy]')) {
    if (el.querySelector('button[data-copy-btn]')) continue;

    el.classList.add('group', 'relative');

    const btn = document.createElement('button');
    btn.type = 'button';
    btn.dataset.copyBtn = '1';
    btn.title = 'Copy';
    btn.setAttribute('aria-label', 'Copy code to clipboard');
    btn.className =
      'absolute right-2.5 top-1/2 grid h-7 w-7 -translate-y-1/2 place-items-center rounded-md ' +
      'border border-border bg-card/90 text-muted-foreground transition hover:text-foreground ' +
      'focus-visible:opacity-100 md:opacity-0 md:group-hover:opacity-100';
    btn.innerHTML = `
      <svg viewBox="0 0 24 24" class="copy-icon h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect>
        <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path>
      </svg>
      <svg viewBox="0 0 24 24" class="check-icon hidden h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <polyline points="20 6 9 17 4 12"></polyline>
      </svg>`;

    btn.addEventListener('click', async () => {
      try {
        await navigator.clipboard.writeText(el.dataset.copy ?? '');
        btn.title = 'Copied';
        btn.querySelector('.copy-icon')?.classList.add('hidden');
        btn.querySelector('.check-icon')?.classList.remove('hidden');
      } catch {
        btn.title = 'Press ⌘C';
      }
      setTimeout(() => {
        btn.title = 'Copy';
        btn.querySelector('.copy-icon')?.classList.remove('hidden');
        btn.querySelector('.check-icon')?.classList.add('hidden');
      }, 1600);
    });

    el.appendChild(btn);
  }
}

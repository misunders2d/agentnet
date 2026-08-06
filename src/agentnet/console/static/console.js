'use strict';

(() => {
  const enrollmentForm = document.querySelector('form[action="/enrollments/review"]');
  if (enrollmentForm) {
    const choices = enrollmentForm.querySelectorAll('input[name="target_kind"]');
    const existing = enrollmentForm.querySelector('[data-enrollment-existing]');
    const invited = enrollmentForm.querySelector('[data-enrollment-new]');
    const updateTarget = () => {
      const selected = enrollmentForm.querySelector('input[name="target_kind"]:checked');
      const useExisting = selected?.value === 'existing_person';
      if (existing) {
        existing.hidden = !useExisting;
        const select = existing.querySelector('select');
        if (select) {
          select.disabled = !useExisting;
          select.required = useExisting;
        }
      }
      if (invited) {
        invited.hidden = useExisting;
        const email = invited.querySelector('input[type="email"]');
        if (email) {
          email.disabled = useExisting;
          email.required = !useExisting;
        }
      }
    };
    choices.forEach((choice) => choice.addEventListener('change', updateTarget));
    updateTarget();
  }

  const copyValue = (target) => {
    if (target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement) {
      return target.value;
    }
    return target.textContent?.trim() || '';
  };

  const fallbackCopy = (value) => {
    const field = document.createElement('textarea');
    field.value = value;
    field.setAttribute('readonly', '');
    field.style.position = 'fixed';
    field.style.insetInlineStart = '-9999px';
    document.body.append(field);
    field.select();
    const copied = document.execCommand('copy');
    field.remove();
    return copied;
  };

  document.querySelectorAll('[data-copy-target]').forEach((button) => {
    button.addEventListener('click', async () => {
      const targetId = button.getAttribute('data-copy-target');
      const statusId = button.getAttribute('data-copy-status');
      const target = targetId ? document.getElementById(targetId) : null;
      const status = statusId ? document.getElementById(statusId) : null;
      if (!target) return;

      const value = copyValue(target);
      let copied = false;
      try {
        if (navigator.clipboard && window.isSecureContext) {
          await navigator.clipboard.writeText(value);
          copied = true;
        } else {
          copied = fallbackCopy(value);
        }
      } catch (_error) {
        copied = false;
      }

      if (status) {
        status.textContent = copied
          ? 'Copied.'
          : 'Could not copy automatically. Select and copy it instead.';
      }
    });
  });

  const invitationContinue = document.querySelector('[data-invitation-continue]');
  if (invitationContinue instanceof HTMLFormElement) {
    invitationContinue.addEventListener('submit', () => {
      const button = invitationContinue.querySelector('button[type="submit"]');
      const status = invitationContinue.querySelector('[data-invitation-continue-status]');
      if (button instanceof HTMLButtonElement) {
        button.disabled = true;
      }
      if (status) {
        status.textContent = ' Opening secure work-account sign-in…';
      }
    });
  }

  const live = document.querySelector('[data-live-status]');
  if (!live) return;

  let revision = Number(live.getAttribute('data-revision') || '0');
  let failures = 0;
  let pollTimer = null;

  const showFreshness = (value) => {
    if (typeof value !== 'number') return;
    const date = new Date(value * 1000);
    live.textContent = `Updated ${date.toLocaleTimeString([], {hour: '2-digit', minute: '2-digit'})}`;
  };

  const apply = (event) => {
    if (!event || typeof event.revision !== 'number') return;
    if (event.revision <= revision) return;
    revision = event.revision;
    showFreshness(event.fresh_at);
    live.textContent += ' — refresh to review changes';
  };

  const schedulePoll = () => {
    if (pollTimer !== null) return;
    const interval = Math.min(60000, 5000 * Math.max(1, 2 ** failures));
    pollTimer = window.setTimeout(async () => {
      pollTimer = null;
      try {
        const response = await fetch(`/v1/console/snapshot?after=${revision}`, {
          credentials: 'same-origin',
          cache: 'no-store',
          headers: {'Accept': 'application/json'},
        });
        if (!response.ok) throw new Error('update unavailable');
        const value = await response.json();
        apply(value);
        failures = 0;
      } catch (_error) {
        failures = Math.min(failures + 1, 4);
      }
      schedulePoll();
    }, interval);
  };

  if ('EventSource' in window) {
    const stream = new EventSource(`/v1/console/events?after=${revision}`, {withCredentials: true});
    stream.addEventListener('console', (event) => {
      try {
        apply(JSON.parse(event.data));
      } catch (_error) {
        failures = Math.min(failures + 1, 4);
      }
    });
    stream.addEventListener('error', () => {
      stream.close();
      schedulePoll();
    });
  } else {
    schedulePoll();
  }
})();

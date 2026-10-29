// A lazy attachment player, scoped to one supplied file slot and captured host
// callbacks. The entry owns admission/download and explicit view replacement.
// Inject the package's existing pictures.mjs sniffVideo; never infer MIME from a name.
export const INLINE_VIDEO_LIMIT = 16 * 1024 * 1024;
export const videoFileName = name => typeof name === 'string' && /\.(mp4|m4v|mov|webm|ogv|ogg)$/i.test(name);

export function mountVideoFile({root, file, load, sniffVideo, download, allowed = () => true, onVisibility}) {
  const document = root.ownerDocument, window = document.defaultView;
  const name = String(file.name || 'Video'), size = file.size;
  const local = file.openable !== false && !['unavailable', 'requestable', 'requested'].includes(file.availability);
  const inline = local && Number.isSafeInteger(size) && size > 0 && size <= INLINE_VIDEO_LIMIT;
  const make = (tag, text = '') => { const node = document.createElement(tag); node.textContent = text; return node; };
  const card = make('div'), label = make('p', name + ' · ' + (Number.isSafeInteger(size) && size >= 0 ? size.toLocaleString() + ' bytes' : 'size unknown'));
  card.className = 'an-video-file'; card.setAttribute('role', 'group'); card.setAttribute('aria-label', 'Video ' + name);
  const actions = make('div'), note = make('p'); note.setAttribute('role', 'status');
  actions.className = 'an-video-actions';
  const button = (text, label) => {
    const node = make('button', text); node.type = 'button'; node.className = 'btn an-hit'; node.setAttribute('aria-label', label);
    return node;
  };
  const play = button('Play', 'Play video ' + name), close = button('Close video', 'Close video ' + name), save = button('Download', 'Download ' + name);
  let active = true, dead = false, pageHidden = false, nativeVisible = true;
  let epoch = 0, pending = false, media = null, owned = '', phase = 'idle', message = '';
  const visible = () => document.visibilityState !== 'hidden' && !document.hidden && !pageHidden && nativeVisible;
  const current = () => !dead && active && root.isConnected !== false && card.isConnected !== false && root.contains(card) && visible() && allowed();
  const paint = () => {
    play.hidden = phase !== 'idle'; close.hidden = phase !== 'loading' && phase !== 'ready';
    play.disabled = close.disabled = save.disabled = dead || !active;
    note.textContent = !local ? 'This video is not available on this device. Use Download to get it.'
      : !inline ? (size > INLINE_VIDEO_LIMIT ? 'Videos up to 16 MiB can play here. Download to play this file.' : 'This video’s size is unknown. Download to play this file.') : message;
    note.hidden = !note.textContent;
  };
  // Invalidate before unloading: synchronous error/late play callbacks see a
  // different epoch and no owned source. Each URL is detached and revoked once.
  const release = (next = 'idle', text = '') => {
    ++epoch; pending = false;
    const video = media, url = owned; media = null; owned = '';
    if (video) {
      try { video.pause(); } catch {}
      video.removeAttribute('src');
      try { video.load(); } catch {}
      video.remove();
    }
    if (url) window.URL.revokeObjectURL(url);
    phase = next; message = text; if (!dead) paint();
  };
  const failed = text => release('failed', text);
  const playbackFailed = (error, visit) => {
    if (epoch !== visit || !current() || !owned) return;
    if (error?.name === 'NotAllowedError') { message = 'Tap Play in the video controls to start.'; paint(); }
    else failed('This video can’t be played here. Download to play it.');
  };
  const open = async () => {
    if (!inline || pending || owned || phase !== 'idle' || !current()) return;
    const visit = ++epoch; pending = true; phase = 'loading'; message = 'Loading video…'; paint();
    try {
      let bytes = (await load()).bytes;
      if (epoch !== visit || !current()) return;
      if (!(bytes instanceof Uint8Array) || bytes.byteLength !== size || bytes.byteLength > INLINE_VIDEO_LIMIT) {
        failed('Video size differs from its attachment details. Download to play it.'); return;
      }
      const type = sniffVideo(bytes);
      if (!['video/mp4', 'video/quicktime', 'video/webm', 'video/ogg'].includes(type)) {
        failed('This video can’t be played here. Download to play it.'); return;
      }
      const blob = new window.Blob([bytes], {type}); bytes = null;
      // Classifier/host callbacks cannot revive a view that left during them.
      if (epoch !== visit || !current()) return;
      owned = window.URL.createObjectURL(blob);
      // The bytes are in memory already: 'auto' lets the engine read them all.
      // With 'none' the Linux app's WebKitGTK streamed the source and froze a few
      // seconds in (owner report, October 29, 2026; file index at its end).
      media = make('video'); media.controls = true; media.playsInline = true; media.preload = 'auto'; media.autoplay = false;
      media.setAttribute('aria-label', name);
      media.className = 'an-video-player';
      const video = media;
      video.addEventListener('error', () => { if (epoch === visit && media === video && owned) failed('This video can’t be played here. Download to play it.'); });
      video.src = owned; card.append(video); phase = 'ready'; message = ''; paint();
      // The explicit click may lose transient activation during async decrypt.
      // A denied gesture keeps native controls; only a later explicit tap plays.
      try { const started = video.play(); started?.catch(error => playbackFailed(error, visit)); }
      catch (error) { playbackFailed(error, visit); }
    } catch {
      if (epoch === visit && current()) failed('Video could not be loaded here. Download to play it.');
    } finally {
      if (epoch === visit) {
        pending = false;
        if (!owned && phase === 'loading') { phase = 'idle'; message = ''; paint(); }
      }
    }
  };
  play.onclick = () => { void open(); };
  close.onclick = () => release();
  save.onclick = async () => {
    if (!current()) return;
    try { await download(); }
    catch { if (current()) { message = 'Download could not be started. Try Download again.'; paint(); } }
  };
  if (inline) actions.append(play, close);
  actions.append(save); card.append(label, actions, note); root.append(card); paint();
  const hidden = () => { if (document.hidden || document.visibilityState === 'hidden') release(); };
  const pagehide = () => { pageHidden = true; release(); };
  const pageshow = () => { pageHidden = false; if (!dead) paint(); };
  document.addEventListener('visibilitychange', hidden);
  window.addEventListener('pagehide', pagehide); window.addEventListener('pageshow', pageshow);
  const offVisibility = onVisibility?.(visible => { nativeVisible = visible; if (!visible) release(); });
  const deactivate = () => { if (dead || !active) return; active = false; release(); };
  const destroy = () => {
    if (dead) return;
    dead = true; active = false; release();
    document.removeEventListener('visibilitychange', hidden);
    window.removeEventListener('pagehide', pagehide); window.removeEventListener('pageshow', pageshow);
    try { offVisibility?.(); } catch {}
    card.remove();
  };
  return {deactivate, destroy};
}

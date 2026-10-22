// Matches internal/ui/media.go. Reserve muxing headroom and audio before
// assigning the video bitrate; refuse unusable quality before codec work.
export function videoBitrate(duration: number, audio: boolean, cap = 32 * 1024 * 1024): number | null {
  if (!(duration > 0 && duration <= 300) || !Number.isFinite(cap) || cap <= 0) return null;
  const rate = Math.min(2_500_000, Math.floor(cap * 8 * .9 / duration) - (audio ? 128_000 : 0));
  return rate >= 650_000 ? rate : null;
}

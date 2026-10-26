// Lazy browser/WebView video processing. BlobSource reads a bounded window;
// muxer output writes into one capped destination, including header rewrites
// needed for complete seekable files. WebCodecs owns the codec work.
import { Input, Output, Mp4OutputFormat, WebMOutputFormat, StreamTarget, BlobSource, Conversion, MP4, WEBM, MATROSKA, canEncodeVideo, canEncodeAudio } from "mediabunny";
import { videoBitrate } from "./Composer.media-budget";

const MAX_OUTPUT = 32 * 1024 * 1024;

export async function compressVideo(file: File, signal: AbortSignal, progress: (n: number) => void): Promise<File> {
  signal.throwIfAborted();
  const input = new Input({ source: new BlobSource(file, { maxCacheSize: 8 * 1024 * 1024 }), formats: [MP4, WEBM, MATROSKA] });
  let conversion: Conversion | undefined;
  let timedOut = false;
  const cancel = () => { input.dispose(); void conversion?.cancel().catch(() => {}); };
  const deadline = setTimeout(() => { timedOut = true; cancel(); }, 120_000);
  signal.addEventListener("abort", cancel, { once: true });
  let bytes: Uint8Array<ArrayBuffer> | undefined;
  let length = 0;
  try {
    const video = await input.getPrimaryVideoTrack(), audio = await input.getPrimaryAudioTrack();
    if (!video || video.displayWidth <= 0 || video.displayHeight <= 0 || video.displayWidth > 4096 || video.displayHeight > 4096 || video.displayWidth * video.displayHeight > 3840 * 2160) throw Error("This video's dimensions exceed the preparation limit.");
    const duration = await input.computeDuration();
    if (!(duration > 0 && duration <= 300)) throw Error("Video preparation supports clips up to five minutes.");
    const bitrate = videoBitrate(duration, !!audio, MAX_OUTPUT);
    if (!bitrate) throw Error("This video's duration cannot fit the output limit at usable quality. Send the original file.");
    const stats = await video.computePacketStats(100);
    const frameRate = Math.min(30, stats.averagePacketRate || 30);
    const scale = Math.min(1, 1280 / Math.max(video.displayWidth, video.displayHeight));
    const width = Math.max(2, Math.floor(video.displayWidth * scale / 2) * 2), height = Math.max(2, Math.floor(video.displayHeight * scale / 2) * 2);
    // Keep mono recordings mono. Besides adding no information, upmixing them
    // to stereo crashes the bundled Linux WebKit audio conversion path.
    const numberOfChannels = audio ? Math.min(2, audio.numberOfChannels) : 2;
    const supported = async (v: "avc" | "vp9", a: "aac" | "opus") => (await Promise.all([
      canEncodeVideo(v, { width, height, frameRate, bitrate }),
      audio ? canEncodeAudio(a, { numberOfChannels, sampleRate: 48000, bitrate: 128_000 }) : true,
    ])).every(Boolean);
    const mp4 = await supported("avc", "aac");
    if (!mp4 && !await supported("vp9", "opus")) throw Error("This device has no supported video/audio encoder. Send the original file.");
    signal.throwIfAborted();
    bytes = new Uint8Array(MAX_OUTPUT);
    const output = new Output({ format: mp4 ? new Mp4OutputFormat({ fastStart: false }) : new WebMOutputFormat(), target: new StreamTarget(new WritableStream({
      write(chunk) {
        if (!Number.isSafeInteger(chunk.position) || chunk.position < 0 || chunk.data.length > MAX_OUTPUT - chunk.position) throw Error("The prepared video exceeds the 32 MiB output limit.");
        bytes!.set(chunk.data, chunk.position); length = Math.max(length, chunk.position + chunk.data.length);
      },
    }), { chunked: true, chunkSize: 1024 * 1024 }) });
    conversion = await Conversion.init({ input, output, tracks: "primary", tags: {}, showWarnings: false,
      video: { codec: mp4 ? "avc" : "vp9", width, height, fit: "contain", frameRate, bitrate, forceTranscode: true, allowTransformationMetadata: false },
      audio: { codec: mp4 ? "aac" : "opus", bitrate: 128_000, numberOfChannels, sampleRate: 48000, forceTranscode: true },
    });
    // A missing audio encoder must not quietly turn an audible clip silent.
    if (!conversion.isValid || conversion.discardedTracks.some(t => t.track === video || audio && t.track === audio)) throw Error("This device cannot encode this video and its audio. Send the original file.");
    signal.throwIfAborted();
    conversion.onProgress = n => { if (timedOut) throw Error("Video preparation reached its two-minute limit."); progress(n); };
    await conversion.execute();
    signal.throwIfAborted();
    if (timedOut) throw Error("Video preparation reached its two-minute limit.");
    return new File([bytes.subarray(0, length)], file.name.replace(/\.[^.]*$/, "") + (mp4 ? ".mp4" : ".webm"), { type: mp4 ? "video/mp4" : "video/webm", lastModified: file.lastModified });
  } finally {
    clearTimeout(deadline); signal.removeEventListener("abort", cancel);
    input.dispose(); bytes = undefined;
    if (conversion && conversion.state !== "done") await conversion.cancel().catch(() => {});
  }
}

import { nativeActivityDetail } from "./NativeActivity.model";

export function NativeActivity({ detail, className = "mx-4 mt-3 text-[14px] text-text-2 [overflow-wrap:anywhere]" }: { detail: string; className?: string }) {
  const activity = nativeActivityDetail(detail);
  if (!activity) return null;
  return <p role="status" data-native-activity className={className}>
    <span>{activity.label}</span>
    <span className="text-[12px] text-muted"> · {activity.at ? <>Native event reported <time dateTime={activity.at} title={activity.at}>{new Date(activity.at).toLocaleTimeString([], { hour: "numeric", minute: "2-digit", second: "2-digit" })}</time></> : "Native activity"}</span>
  </p>;
}

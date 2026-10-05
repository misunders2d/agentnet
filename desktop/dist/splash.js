// The app's starting page: "Starting AgentNet…", or, when the shell puts
// #error=… in the address, what went wrong and Try again (which the shell
// sees as a navigation to ?retry=1).
const m = /^#error=(.*)$/.exec(location.hash);
if (m) {
  let text = "";
  try { text = decodeURIComponent(m[1].replace(/\+/g, " ")); } catch (e) { text = "AgentNet stopped."; }
  document.querySelector("h1").textContent = "AgentNet needs a moment";
  const p = document.querySelector("p");
  p.textContent = text;
  const a = document.createElement("a");
  a.href = "index.html?retry=1";
  a.textContent = "Try again";
  a.className = "retry";
  p.after(a);
}

// Google's standard browser sign-in button. The credential stays in the
// callback's memory and is bound to this browser's device keys by nonce.
let loading;
async function gis() {
  if (globalThis.google?.accounts?.id) return google.accounts.id;
  if (!loading) loading = new Promise((resolve, reject) => {
    const s = document.createElement("script");
    s.src = "https://accounts.google.com/gsi/client";
    s.async = true;
    s.onload = () => globalThis.google?.accounts?.id ? resolve(google.accounts.id) : reject(new Error("Google sign-in did not load."));
    s.onerror = () => { loading = null; reject(new Error("Google sign-in could not load. Check your connection and try again.")); };
    document.head.append(s);
  });
  return loading;
}

export async function mountGoogleSignIn(root, clientID, nonce, onCredential) {
  const api = await gis();
  let busy = false;
  const error = document.createElement("p"); error.className = "error"; error.setAttribute("role", "alert");
  const button = document.createElement("div");
  root.replaceChildren(button, error);
  api.initialize({ client_id: clientID, nonce, auto_select: false, callback: async ({ credential }) => {
    if (busy) return;
    busy = true; button.inert = true; error.textContent = "Joining…";
    try { await onCredential(credential); }
    catch (e) { error.textContent = e.message || "Google sign-in failed. Try again."; }
    finally { busy = false; button.inert = false; }
  } });
  api.renderButton(button, { type: "standard", theme: "outline", size: "large", text: "signin_with", width: Math.min(320, root.clientWidth || 320) });
}

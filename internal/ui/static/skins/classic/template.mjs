export const markup = `
<a class="skip" href="#timeline">Skip to messages</a>

<div id="demo" class="demo" role="note" hidden>
  <span class="demo-label">Demo</span>
  <span class="demo-text">Invented people and messages. Nothing is sent, run or saved.</span>
  <span class="demo-controls">
    <button type="button" id="sim-arrival" class="chip">Simulate a message</button>
    <button type="button" id="sim-finish" class="chip">Simulate responder finishing</button>
  </span>
</div>

<div id="offline" class="lost offline" role="status" hidden></div>
<div id="lost" class="lost" role="alert" hidden>
  <span>Lost connection to agentnet. You are seeing what the page last loaded.</span>
  <button type="button" id="reconnect" class="chip">Reconnect</button>
</div>

<div id="updating" class="demo" role="status" hidden>
  <span id="updating-text">AgentNet is switching to its updated version. Reconnecting…</span>
  <button type="button" id="reload" class="chip" hidden>Reload</button>
</div>

<div class="workspace">
<nav class="nav-rail" aria-label="Main navigation">
  <img class="rail-logo" src="${new URL('./icon.png', import.meta.url).href}" alt="AgentNet" width="32" height="32">
  <button id="nav-chats" class="nav-item selected" type="button" aria-current="page"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 4h16v12H9l-5 4zM8 8h8M8 12h5"/></svg><span class="nav-label">Chats</span></button>
  <button id="nav-people" class="nav-item" type="button"><svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="9" cy="8" r="3"/><path d="M3 20v-2a6 6 0 0 1 12 0v2M16 5a3 3 0 0 1 0 6M18 14a5 5 0 0 1 3 4v2"/></svg><span class="nav-label">People</span></button>
        <div class="review-wrap">
          <button type="button" id="review-btn" class="review-btn" aria-expanded="false" aria-controls="review">
            <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 5h14v14H5zM8 9h8M8 13h5"/></svg><span class="nav-label">Activity</span><span id="review-word" class="sr-only">Needs you</span>
            <span class="review-counts">
              <span id="review-reports" class="count reports" hidden></span>
              <span id="review-count" class="count">0</span>
            </span>
          </button>

        </div>
  <button id="profile-btn" class="nav-item profile-btn" type="button" aria-label="Your profile and settings"><span id="profile-initial" class="profile-initial">A</span><span class="nav-label">You</span></button>
</nav>
<div class="view-stack">
<div class="app">
  <aside class="side" aria-label="Conversations">
    <header class="side-head">
      <h1 id="section-title">Chats</h1>
      <div class="side-actions">
        <button type="button" id="new-btn" class="new-btn" aria-label="New conversation" title="New conversation">
          <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><path d="M12 5v14M5 12h14" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"/></svg>
        </button>
      </div>
    </header>

    <div class="search">
      <label class="sr-only" for="search">Search people, agents and conversations</label>
      <input id="search" type="search" placeholder="Search people, agents and conversations" autocomplete="off" spellcheck="false">
    </div>
          <div id="review" class="review" hidden>
            <h2>Waiting for your decision</h2>
            <p class="hint">Each one opens in its conversation, where you decide.</p>
            <ul id="review-list"></ul><ul id="activity-extra"></ul>
            <div id="reports" hidden>
              <h2>Reported by other machines</h2>
              <p class="hint">Each report is what that machine sent at that time, not a live queue. Each one says whether you can decide its requests from here.</p>
              <ul id="report-list"></ul>
              <details id="reports-earlier" class="reports-earlier" hidden>
                <summary>Earlier reports, dismissed</summary>
                <ul id="report-earlier-list"></ul>
              </details>
            </div>
          </div>
    <h2 class="sr-only" id="list-title">Contacts</h2>
    <ul id="conv-list" class="conv-list" aria-labelledby="list-title"></ul>


  </aside>

  <main class="conv" id="conv" aria-labelledby="conv-name">
    <header class="conv-head">
      <button type="button" id="back" class="icon-btn back" aria-label="Back to conversations">
        <svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><path d="M15 5l-7 7 7 7" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>
      </button>
      <span id="conv-avatar" class="avatar" aria-hidden="true"></span>
      <div class="conv-title">
        <button type="button" id="hub-back" class="text-btn hub-back" hidden></button>
        <h1 id="conv-name">Choose a conversation</h1>
        <p id="conv-topic" class="topic"></p>
        <p id="conv-presence" class="presence"></p>
        <p id="typing-line" class="presence" hidden></p>
      </div>
      <div id="peer-chips" class="peer-chips"></div><button id="conversation-details" class="icon-btn" type="button" aria-label="Conversation details">⋯</button>
    </header>
    <div id="notice" class="notice" role="alert" hidden></div>
    <section id="agents" class="agents" aria-label="Conversation participants" hidden></section>
    <section id="drive-panel" class="drive-panel" aria-label="Project space" hidden></section>
    <section id="hub" class="hub" aria-labelledby="conv-name" hidden></section>
    <ol id="timeline" class="timeline" tabindex="-1" aria-label="Messages"></ol>

    <form id="composer" class="composer" hidden>
      <div id="target" class="target" aria-live="polite">
        <span id="to-line" class="to-line"><span class="to-label">To</span> <span id="to-name" class="to-name"></span> <span id="to-how" class="to-how"></span></span>
        <div id="agent-target" class="agent-target" hidden></div>

      </div>
      <div id="replying" class="replying" hidden>
        <span class="replying-text"><span id="replying-label">Answering</span> <span id="replying-text"></span></span>
        <button type="button" id="replying-cancel" class="text-btn">Cancel</button>
      </div>
      <div class="compose-box">
        <ul id="attach-list" class="attach-list" aria-label="Files to send" hidden></ul>
        <label class="sr-only" for="body">Your message</label>
        <div id="mentions" class="mentions" role="group" aria-label="Added assistants" hidden></div>
        <textarea id="body" rows="1" placeholder="Write a message" aria-controls="mentions" aria-expanded="false"></textarea>
        <div class="compose-bar">
          <button type="button" id="attach" class="icon-btn attach" aria-label="Attach images or files" title="Attach images or files (or paste an image)">
            <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><path d="M21 11.5l-8.6 8.6a5 5 0 0 1-7.1-7.1l8.6-8.6a3.3 3.3 0 0 1 4.7 4.7l-8.6 8.6a1.7 1.7 0 0 1-2.4-2.4l7.9-7.9" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>
          </button>
          <button type="button" id="mention-button" class="icon-btn" aria-label="Mention an added assistant" title="Mention an added assistant">@</button>
          <input type="file" id="file-input" multiple hidden>
          <fieldset id="kind" class="kind" hidden>
            <legend class="sr-only">Send as</legend>
            <label><input type="radio" name="kind" value="message" checked><span>Message</span></label>
            <label><input type="radio" name="kind" value="question"><span>Question</span></label>
          </fieldset>
          <button type="submit" id="send" class="send">
            <span id="send-label">Send</span>
            <svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true"><path d="M5 12h13M13 6l6 6-6 6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>
          </button>
        </div>
      </div>
      <div class="compose-foot">
        <span id="compose-hint" class="hint"></span>
      </div>
      <p id="compose-error" class="error" role="alert"></p>
    </form>
  </main>
</div>

</div>
</div>

<div id="live" class="sr-only" aria-live="polite"></div>

<dialog id="routing-settings" class="settings" aria-labelledby="routing-title">
  <header class="settings-head"><h2 id="routing-title">Advanced CLI reply routing</h2><button type="button" id="routing-close" class="icon-btn" aria-label="Close reply routing">×</button></header>
  <section class="settings-panel"><p class="hint">Optional continuation for this draft only. Human replies stay in this conversation by default. CLI-origin bindings stay exact; no assistant or backup is chosen automatically.</p>
        <details id="receiver-options" class="receiver-options" hidden>
          <summary id="receiver-summary">Reply receiver · Me (human)</summary>
          <div class="receiver-options-body">
            <div id="reply-receiver" class="reply-receiver" hidden></div>
            <div id="receiver-status" class="receiver-status" hidden></div>
          </div>
        </details>
  </section>
</dialog>

<dialog id="settings" class="settings" aria-labelledby="settings-title">
  <header class="settings-head"><h2 id="settings-title">Your profile & settings</h2><button id="settings-close" type="button" class="icon-btn" aria-label="Close settings">✕</button></header>
  <nav id="settings-tabs" class="settings-tabs" role="tablist" aria-label="Settings sections">
    <button type="button" role="tab" id="settings-tab-profile" aria-controls="settings-profile" data-settings="profile" aria-pressed="true">Profile</button>
    <button type="button" role="tab" id="settings-tab-appearance" aria-controls="settings-appearance" data-settings="appearance" aria-pressed="false">Appearance</button>
    <button type="button" role="tab" id="settings-tab-notifications" aria-controls="settings-notifications" data-settings="notifications" aria-pressed="false">Notifications</button>
    <button type="button" role="tab" id="settings-tab-device" aria-controls="settings-device" data-settings="device" aria-pressed="false">Assistants</button>
    <button type="button" role="tab" id="settings-tab-storage" aria-controls="settings-storage" data-settings="storage" aria-pressed="false">Storage</button>
  </nav>
  <section id="settings-profile" role="tabpanel" aria-labelledby="settings-tab-profile" class="settings-panel"><div id="profile-card"></div><p id="release" class="release" hidden></p><div id="profile-devices"></div><div id="app-update"></div></section>
  <section id="settings-appearance" role="tabpanel" aria-labelledby="settings-tab-appearance" class="settings-panel" hidden>
    <h3>Interface</h3><p class="hint">Different ways to use the same conversations. Installed interfaces appear here too.</p>
      <div id="interfaces" class="interfaces-switch" role="group" aria-label="Interface">


      </div>
    <h3>Color theme</h3><div id="theme-choice" class="settings-tabs"><button type="button" data-theme="system">System</button><button type="button" data-theme="light">Light</button><button type="button" data-theme="dark">Dark</button></div>
    <p class="hint">Appearance is saved in this browser.</p>
    <div id="local-interfaces"></div>
  </section>
  <section id="settings-notifications" role="tabpanel" aria-labelledby="settings-tab-notifications" class="settings-panel" hidden><div id="typing-settings"></div></section>
  <section id="settings-storage" role="tabpanel" aria-labelledby="settings-tab-storage" class="settings-panel" hidden>
    <h3>Where your files live</h3>
    <p class="hint">What AgentNet holds for you here and on your server, read once when you open this. Nothing is cleaned, moved or changed by looking.</p>
    <div id="storage"></div>
    <h3>File storage options</h3>
    <p class="hint">Optional Google Drive for project folders, off unless the workspace admin turns it on. Encrypted attachments never depend on it. Files in a Drive folder are outside AgentNet's encryption.</p>
    <div id="file-storage"></div>
  </section>
  <section id="settings-device" role="tabpanel" aria-labelledby="settings-tab-device" class="settings-panel" hidden>
    <div id="app-command"></div>
    <div id="assistant-setup"></div>
    <h3>Your agent on this computer</h3>
    <p class="hint">Who answers approved questions and runs accepted tasks here. Installed means found on this computer, not logged in or working.</p>
    <div id="responder"></div>
    <div id="named-agents"></div>
    <details class="machine-more"><summary>Technical details</summary><div id="me" class="mono"></div></details>
    <footer class="side-foot">
      <details id="quarantine" class="held-back" hidden>
        <summary id="quarantine-summary"></summary>
        <ul id="quarantine-list"></ul>
      </details>
      <p id="notify-line" class="notify-line" hidden></p>
      <details id="machine-more" class="machine-more">
        <summary id="machine" class="machine"></summary>
        <div id="machine-detail" class="machine-detail"></div>
      </details>
    </footer>  </section>
</dialog>

<dialog id="dialog" aria-labelledby="dialog-title">
  <form method="dialog" id="dialog-form">
    <h2 id="dialog-title"></h2>
    <div id="dialog-body"></div>
    <p id="dialog-error" class="error" role="alert"></p>
    <div class="dialog-actions">
      <button type="submit" value="cancel" id="dialog-cancel" class="btn">Cancel</button>
      <button type="button" id="dialog-ok" class="btn primary"></button>
    </div>
  </form>
</dialog>
`;

/* Timer clicks lack a user gesture. Permit popups only in this disposable
 * fixture so the real WebKit create callback and Rust opener can be tested. */
#define _GNU_SOURCE
#include <webkit2/webkit2.h>
#include <dlfcn.h>
#include <stdlib.h>
#include <stdio.h>

WebKitSettings *webkit_web_view_get_settings(WebKitWebView *view) {
 typedef WebKitSettings *(*get_fn)(WebKitWebView *);
 get_fn get=(get_fn)dlsym(RTLD_NEXT,"webkit_web_view_get_settings");
 WebKitSettings *settings=get(view);
 const char *evidence=getenv("AGENTNET_FIXTURE_POPUP_EVIDENCE");
 if(evidence && !webkit_settings_get_javascript_can_open_windows_automatically(settings)) {
  FILE *log=fopen(evidence,"a");
  if(log){fputs("automatic_popups_before=0 fixture_enabled=1\n",log);fclose(log);}
  webkit_settings_set_javascript_can_open_windows_automatically(settings,TRUE);
 }
 return settings;
}

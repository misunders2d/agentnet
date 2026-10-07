/* Test-only instrumentation of the fixture process's real native chooser. */
#define _GNU_SOURCE
#include <gtk/gtk.h>
#include <dlfcn.h>
#include <stdlib.h>
#include <stdio.h>
#include <string.h>
static gboolean choose(gpointer data) {
 GtkFileChooserNative *dialog=data;
 const char *folder=getenv("AGENTNET_FIXTURE_FOLDER"),*evidence=getenv("AGENTNET_FIXTURE_NATIVE_EVIDENCE");
 if(!folder||!evidence)return G_SOURCE_REMOVE;
 FILE *log=fopen(evidence,"a");
 if(log){fprintf(log,"action=%d visible=%d\n",gtk_file_chooser_get_action(GTK_FILE_CHOOSER(dialog)),gtk_native_dialog_get_visible(GTK_NATIVE_DIALOG(dialog)));fclose(log);}
 gtk_file_chooser_set_filename(GTK_FILE_CHOOSER(dialog),folder);
 gchar *selected=gtk_file_chooser_get_filename(GTK_FILE_CHOOSER(dialog));
 if(!selected || strcmp(selected,folder)){g_free(selected);return G_SOURCE_CONTINUE;}
 g_free(selected);
 g_signal_emit_by_name(dialog,"response",GTK_RESPONSE_ACCEPT);
 return G_SOURCE_REMOVE;
}
GtkFileChooserNative *gtk_file_chooser_native_new(const gchar *title,GtkWindow *parent,GtkFileChooserAction action,const gchar *accept,const gchar *cancel) {
 typedef GtkFileChooserNative *(*create_fn)(const gchar*,GtkWindow*,GtkFileChooserAction,const gchar*,const gchar*);
 create_fn create=(create_fn)dlsym(RTLD_NEXT,"gtk_file_chooser_native_new");
 GtkFileChooserNative *dialog=create(title,parent,action,accept,cancel);
 if(title && !strcmp(title,"Choose an AgentNet skin folder"))g_timeout_add(600,choose,dialog);
 return dialog;
}

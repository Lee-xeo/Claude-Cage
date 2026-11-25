/* Native Linux host for the shared Cage workbench. */
#define _POSIX_C_SOURCE 200809L
#include <gtk/gtk.h>
#include <webkit2/webkit2.h>
#include <json-glib/json-glib.h>
#include <glib/gstdio.h>
#include <unistd.h>
#include <string.h>
#include <stdlib.h>
#include <sys/stat.h>
#include <math.h>
#include "native-input.h"
#include "media-plugins.h"

#ifndef CAGE_VERSION
#define CAGE_VERSION "development"
#endif
#ifndef CAGE_CLIENT_ONLY
#define CAGE_CLIENT_ONLY 0
#endif
#define MAX_THEME (256 * 1024)
#define MAX_MESSAGE (2 * 1024 * 1024)

typedef struct {
    GtkApplication *application;
    GtkWidget *window, *stack, *status, *retry, *existing, *complete;
    GMenu *menu;
    JsonObject *messages;
    WebKitWebView *web;
    WebKitWebContext *web_context;
    WebKitUserContentManager *content;
    GdkRGBA background_color;
    char *backend, *profile, *token, *smoke_output, *web_data, *web_cache;
    const char *storage_phase, *storage_marker;
    int port, result;
    guint backend_commands;
    gboolean bootstrapping, needs_update, ready, picker_open, smoke_started, smoke_finishing, navigation_failed;
    gboolean initial_dark, media_plugins_isolated, dmabuf_default_set, software_fallback, has_background_color, quit_pending;
} Cage;
static Cage app;
typedef struct { GPtrArray *paths; gint64 expires; } FileSelection;
static GHashTable *file_selections;
static guint64 clipboard_revision = 1;
static guint64 clipboard_generation = 0;
static gboolean clipboard_watching = FALSE;
typedef struct { GPtrArray *paths; gboolean pending, releasing; } ClipboardCopy;
static GHashTable *clipboard_copies;
static GPtrArray *clipboard_readers;
static GPtrArray *clipboard_sends;
static gboolean clipboard_cache_loaded = FALSE, clipboard_cache_collecting = FALSE;
static void clipboard_cache_watch(void);
static void clipboard_cache_ignored(JsonObject *object, const GError *error, gpointer data);
static void clipboard_changed(GtkClipboard *clipboard, GdkEventOwnerChange *event, gpointer data) {
    (void)clipboard; (void)event; (void)data; clipboard_revision++;
}
static guint64 clipboard_current_revision(void) {
    clipboard_cache_watch();
    if (!clipboard_watching) {
        g_signal_connect(gtk_clipboard_get(GDK_SELECTION_CLIPBOARD), "owner-change", G_CALLBACK(clipboard_changed), NULL);
        clipboard_watching = TRUE;
    }
    return clipboard_revision;
}
static void bootstrap(const char *option);
static void show_error(const char *message);
static void update_menu(void);

typedef void (*BackendCallback)(JsonObject *, const GError *, gpointer);
typedef struct {
    GSubprocess *process;
    GCancellable *cancel;
    guint deadline;
    gsize output_limit;
    BackendCallback callback;
    gpointer data;
} Command;

static const char *text_member(JsonObject *object, const char *name) {
    JsonNode *node = object ? json_object_get_member(object, name) : NULL;
    return node && JSON_NODE_HOLDS_VALUE(node) && json_node_get_value_type(node) == G_TYPE_STRING
        ? json_node_get_string(node) : "";
}
static const char *ui_text(const char *source) {
    const char *message = text_member(app.messages, source);
    return *message ? message : source;
}
/* Translations are whole sentences; only the {0} detail is inserted, so word order, spacing and punctuation stay per-language. */
static char *ui_format(const char *source, const char *detail) {
    const char *text = ui_text(source);
    const char *slot = strstr(text, "{0}");
    return slot ? g_strdup_printf("%.*s%s%s", (int)(slot - text), text, detail, slot + 3) : g_strdup(text);
}
static void set_messages(JsonObject *result) {
    JsonNode *node = result ? json_object_get_member(result, "messages") : NULL;
    if (!node || !JSON_NODE_HOLDS_OBJECT(node)) return;
    g_clear_pointer(&app.messages, json_object_unref);
    app.messages = json_object_ref(json_node_get_object(node));
}
static void load_language(void) {
    char *arguments[] = {app.backend, "interface-language", "--json", NULL};
    char *output = NULL;
    int status = 0;
    if (g_spawn_sync(NULL, arguments, NULL, G_SPAWN_STDERR_TO_DEV_NULL, NULL, NULL, &output, NULL, &status, NULL)
            && g_spawn_check_wait_status(status, NULL)) {
        JsonParser *parser = json_parser_new();
        if (json_parser_load_from_data(parser, output, -1, NULL) && JSON_NODE_HOLDS_OBJECT(json_parser_get_root(parser)))
            set_messages(json_node_get_object(json_parser_get_root(parser)));
        g_object_unref(parser);
    }
    g_free(output);
}
static char *json_text(JsonNode *node) {
    JsonGenerator *generator = json_generator_new();
    json_generator_set_root(generator, node);
    char *text = json_generator_to_data(generator, NULL);
    g_object_unref(generator);
    return text;
}
static char *quote(const char *value) {
    JsonNode *node = json_node_new(JSON_NODE_VALUE);
    json_node_set_string(node, value);
    char *result = json_text(node);
    json_node_free(node);
    return result;
}
static void diagnostic_sent(GObject *source, GAsyncResult *result, gpointer data) {
    (void)data;
    g_subprocess_communicate_utf8_finish(G_SUBPROCESS(source), result, NULL, NULL, NULL);
}
static void sanitize_child_media_env(GSubprocessLauncher *launcher) {
    if (app.media_plugins_isolated) {
        g_subprocess_launcher_unsetenv(launcher, "GST_PLUGIN_SYSTEM_PATH_1_0");
        g_subprocess_launcher_unsetenv(launcher, "GST_REGISTRY_1_0");
    }
    if (app.dmabuf_default_set)
        g_subprocess_launcher_unsetenv(launcher, "WEBKIT_DISABLE_DMABUF_RENDERER");
}
static void diagnostic_event(const char *message) {
    if (!app.backend || !app.profile) return;
    GSubprocessLauncher *launcher = g_subprocess_launcher_new(G_SUBPROCESS_FLAGS_STDIN_PIPE | G_SUBPROCESS_FLAGS_STDOUT_SILENCE | G_SUBPROCESS_FLAGS_STDERR_SILENCE);
    sanitize_child_media_env(launcher);
    g_subprocess_launcher_setenv(launcher, "CLAUDE_CAGE_CONFIG", app.profile, TRUE);
    GSubprocess *process = g_subprocess_launcher_spawn(launcher, NULL, app.backend, "diagnostic-event", NULL);
    g_object_unref(launcher);
    if (!process) return;
    char *limited = g_utf8_substring(message, 0, MIN(g_utf8_strlen(message, -1), 4096));
    char *quoted = quote(limited);
    char *body = g_strdup_printf("{\"source\":\"native\",\"message\":%s}", quoted);
    g_subprocess_communicate_utf8_async(process, body, NULL, diagnostic_sent, NULL);
    g_object_unref(process); g_free(body); g_free(quoted); g_free(limited);
}
static void diagnostic_glib(const gchar *domain, GLogLevelFlags level, const gchar *message, gpointer data) {
    (void)data;
    if (level & (G_LOG_LEVEL_ERROR | G_LOG_LEVEL_CRITICAL | G_LOG_LEVEL_WARNING)) diagnostic_event(message);
    g_log_default_handler(domain, level, message, NULL);
}
static gboolean valid_workbench(const char *raw, int expected_port, gboolean entry) {
    if (!raw) return FALSE;
    GUri *uri = g_uri_parse(raw, G_URI_FLAGS_NONE, NULL);
    if (!uri) return FALSE;
    int port = g_uri_get_port(uri);
    const char *fragment = g_uri_get_fragment(uri);
    gboolean valid = g_strcmp0(g_uri_get_scheme(uri), "http") == 0 &&
        g_strcmp0(g_uri_get_host(uri), "localhost") == 0 &&
        !g_uri_get_userinfo(uri) && port >= 1024 && port <= 65535 &&
        (!expected_port || expected_port == port) &&
        (!entry || (fragment && strlen(fragment) >= 2));
    g_uri_unref(uri);
    return valid;
}
static gboolean private_directory(const char *path) {
    if (g_mkdir_with_parents(path, 0700) != 0) return FALSE;
    GFile *file = g_file_new_for_path(path);
    GFileInfo *info = g_file_query_info(file, G_FILE_ATTRIBUTE_STANDARD_TYPE "," G_FILE_ATTRIBUTE_UNIX_UID,
        G_FILE_QUERY_INFO_NOFOLLOW_SYMLINKS, NULL, NULL);
    gboolean valid = info && g_file_info_get_file_type(info) == G_FILE_TYPE_DIRECTORY &&
        g_file_info_get_attribute_uint32(info, G_FILE_ATTRIBUTE_UNIX_UID) == geteuid() && g_chmod(path, 0700) == 0;
    g_clear_object(&info); g_object_unref(file);
    return valid;
}
static gboolean prepare_web_storage(void) {
    char *profile_directory = g_path_get_dirname(app.profile);
    char *root = g_build_filename(profile_directory, "webkit", NULL);
    app.web_data = g_build_filename(root, "data", NULL);
    app.web_cache = g_build_filename(root, "cache", NULL);
    gboolean valid = private_directory(profile_directory) && private_directory(root) &&
        private_directory(app.web_data) && private_directory(app.web_cache);
    g_free(root); g_free(profile_directory);
    return valid;
}
static gboolean command_timeout(gpointer data) {
    Command *command = data;
    command->deadline = 0;
    g_cancellable_cancel(command->cancel);
    g_subprocess_force_exit(command->process);
    return G_SOURCE_REMOVE;
}
static void command_finished(GObject *source, GAsyncResult *result, gpointer data) {
    Command *command = data;
    GError *error = NULL;
    char *output = NULL;
    JsonParser *parser = json_parser_new();
    JsonObject *object = NULL;
    if (command->deadline) g_source_remove(command->deadline);
    app.backend_commands--;
    if (clipboard_sends) g_ptr_array_remove(clipboard_sends, command);
    if (g_subprocess_communicate_utf8_finish(G_SUBPROCESS(source), result, &output, NULL, &error)) {
        if (!output || strlen(output) > command->output_limit || !json_parser_load_from_data(parser, output, -1, NULL) ||
            !JSON_NODE_HOLDS_OBJECT(json_parser_get_root(parser))) {
            g_set_error_literal(&error, G_IO_ERROR, G_IO_ERROR_INVALID_DATA, "Cage 后台未返回有效状态，请查看私有配置中的 logs/。");
        } else {
            object = json_node_get_object(json_parser_get_root(parser));
            if (!g_subprocess_get_successful(command->process)) {
                const char *message = text_member(object, "message");
                g_set_error_literal(&error, G_IO_ERROR, G_IO_ERROR_FAILED,
                    *message ? message : "Cage 后台操作失败，请查看 logs/。");
            }
        }
    }
    command->callback(object, error, command->data);
    g_clear_error(&error);
    g_free(output);
    g_object_unref(parser);
    g_object_unref(command->process);
    g_object_unref(command->cancel);
    g_free(command);
}
static Command *backend_arguments_limited(const char *const *args, guint seconds, gsize output_limit, BackendCallback callback, gpointer data) {
    GError *error = NULL;
    GSubprocessLauncher *launcher = g_subprocess_launcher_new(G_SUBPROCESS_FLAGS_STDOUT_PIPE | G_SUBPROCESS_FLAGS_STDERR_SILENCE);
    sanitize_child_media_env(launcher);
    g_subprocess_launcher_setenv(launcher, "CLAUDE_CAGE_CONFIG", app.profile, TRUE);
    if (g_strcmp0(args[1], "desktop") == 0) {
        char owner[32];
        g_snprintf(owner, sizeof(owner), "%ld", (long)getpid());
        g_subprocess_launcher_setenv(launcher, "CAGE_DESKTOP_OWNER_PID", owner, TRUE);
    }
    GSubprocess *process = g_subprocess_launcher_spawnv(launcher, args, &error);
    g_object_unref(launcher);
    if (!process) {
        callback(NULL, error, data);
        g_clear_error(&error);
        return NULL;
    }
    Command *command = g_new0(Command, 1);
    command->process = process;
    command->cancel = g_cancellable_new();
    command->callback = callback;
    command->data = data;
    command->output_limit = output_limit;
    command->deadline = g_timeout_add_seconds(seconds, command_timeout, command);
    app.backend_commands++;
    g_subprocess_communicate_utf8_async(process, NULL, command->cancel, command_finished, command);
    return command;
}
static Command *backend_arguments(const char *const *args, guint seconds, BackendCallback callback, gpointer data) {
    return backend_arguments_limited(args, seconds, 65536, callback, data);
}
static void backend(const char *action, const char *option, BackendCallback callback, gpointer data) {
    const char *args[] = {app.backend, action, "--json", option, NULL};
    backend_arguments(args, 180, callback, data);
}
static void script(const char *code) {
    if (app.web && app.port && valid_workbench(webkit_web_view_get_uri(app.web), app.port, FALSE))
        webkit_web_view_evaluate_javascript(app.web, code, -1, NULL, NULL, NULL, NULL, NULL);
}
static void action(const char *name) {
    char *quoted = quote(name);
    char *code = g_strdup_printf("window.cageNativeAction?.(%s)", quoted);
    script(code);
    g_free(code);
    g_free(quoted);
}
static void publish_focus(void) {
    if (!app.window) return;
    gboolean focused = gtk_widget_get_visible(app.window) && gtk_window_is_active(GTK_WINDOW(app.window));
    script(focused ?
        "window.cageWindowFocused=true;window.dispatchEvent(new Event('cage-window-focus'));" :
        "window.cageWindowFocused=false;window.dispatchEvent(new Event('cage-window-focus'));");
}
static void focus_changed(GObject *object, GParamSpec *spec, gpointer data) {
    (void)object; (void)spec; (void)data;
    publish_focus();
}
static void publish_visibility(void) {
    GdkWindow *window = gtk_widget_get_window(app.window);
    gboolean visible = gtk_widget_get_visible(app.window) && window &&
        !(gdk_window_get_state(window) & GDK_WINDOW_STATE_ICONIFIED);
    script(visible ?
        "window.cageWindowVisible=true;window.dispatchEvent(new Event('cage-window-visibility'));" :
        "window.cageWindowVisible=false;window.dispatchEvent(new Event('cage-window-visibility'));" );
    publish_focus();
}
static gboolean visibility_changed(GtkWidget *widget, GdkEvent *event, gpointer data) {
    (void)widget; (void)event; (void)data;
    publish_visibility();
    return FALSE;
}
static gboolean close_window(GtkWidget *widget, GdkEvent *event, gpointer data) {
    (void)event; (void)data;
    gtk_widget_hide(widget);
    publish_visibility();
    return TRUE;
}
static gboolean quit_idle(gpointer data) {
    (void)data;
    g_application_quit(G_APPLICATION(app.application));
    return G_SOURCE_REMOVE;
}
static void show_error(const char *message) {
    diagnostic_event(message);
    gtk_label_set_text(GTK_LABEL(app.status), ui_text(message));
    gtk_stack_set_visible_child_name(GTK_STACK(app.stack), "status");
    gtk_widget_show(app.retry);
}
static void reply_json(WebKitScriptMessageReply *reply, JSCContext *context, const char *json) {
    JSCValue *value = jsc_value_new_from_json(context, json);
    webkit_script_message_reply_return_value(reply, value);
    g_object_unref(value);
}
static void reply_string(WebKitScriptMessageReply *reply, JSCContext *context, const char *text) {
    JSCValue *value = jsc_value_new_string(context, text);
    webkit_script_message_reply_return_value(reply, value);
    g_object_unref(value);
}
static void reply_error(WebKitScriptMessageReply *reply, const char *message) {
    webkit_script_message_reply_return_error_message(reply, message);
}
static char *choose_file(GtkFileChooserAction kind, const char *title, const char *filename) {
    if (app.picker_open) return NULL;
    app.picker_open = TRUE;
    GtkWidget *dialog = gtk_file_chooser_dialog_new(ui_text(title), GTK_WINDOW(app.window), kind,
        ui_text("取消"), GTK_RESPONSE_CANCEL, ui_text(kind == GTK_FILE_CHOOSER_ACTION_SAVE ? "保存" : "选择"), GTK_RESPONSE_ACCEPT, NULL);
    gtk_file_chooser_set_local_only(GTK_FILE_CHOOSER(dialog), TRUE);
    if (kind == GTK_FILE_CHOOSER_ACTION_SAVE) {
        char *base = g_path_get_basename(filename && *filename ? filename : "Cage.cage-theme.json");
        gtk_file_chooser_set_current_name(GTK_FILE_CHOOSER(dialog), base);
        gtk_file_chooser_set_do_overwrite_confirmation(GTK_FILE_CHOOSER(dialog), TRUE);
        g_free(base);
    }
    if (kind != GTK_FILE_CHOOSER_ACTION_SELECT_FOLDER) {
        GtkFileFilter *filter = gtk_file_filter_new();
        gboolean diagnostic = filename && g_str_has_suffix(filename, ".zip");
        gtk_file_filter_set_name(filter, ui_text(diagnostic ? "诊断包 (*.zip)" : "Cage 主题 (*.json)"));
        gtk_file_filter_add_pattern(filter, diagnostic ? "*.zip" : "*.json");
        gtk_file_chooser_add_filter(GTK_FILE_CHOOSER(dialog), filter);
    }
    char *path = gtk_dialog_run(GTK_DIALOG(dialog)) == GTK_RESPONSE_ACCEPT
        ? gtk_file_chooser_get_filename(GTK_FILE_CHOOSER(dialog)) : NULL;
    gtk_widget_destroy(dialog);
    app.picker_open = FALSE;
    return path;
}
typedef struct { WebKitScriptMessageReply *reply; JSCContext *context; gboolean sync, defer_files; guint64 revision, generation; char *connection; GPtrArray *clipboard_paths; } PendingReply;
static PendingReply *pending_reply(WebKitScriptMessageReply *reply, JSCContext *context) {
    PendingReply *pending = g_new0(PendingReply, 1);
    pending->reply = webkit_script_message_reply_ref(reply);
    pending->context = g_object_ref(context);
    return pending;
}
static void pending_free(PendingReply *pending) {
    if (pending->clipboard_paths) {
        g_ptr_array_remove(clipboard_readers, pending->clipboard_paths);
        g_ptr_array_unref(pending->clipboard_paths);
    }
    webkit_script_message_reply_unref(pending->reply);
    g_object_unref(pending->context);
    g_free(pending->connection);
    g_free(pending);
}
/* Notifications go straight to the freedesktop notification service. GNotification
   would route through org.gtk.Notifications on GNOME, which drops notifications
   whose application ID has no matching .desktop file, and it cannot report that.
   Each notification keeps its own click target, keyed by the server's ID. */
#define NOTIFY_NAME "org.freedesktop.Notifications"
#define NOTIFY_PATH "/org/freedesktop/Notifications"
#define NOTIFY_TARGETS 128
static GDBusConnection *notify_bus;
static GHashTable *notify_targets, *notify_tokens;
static void open_notification_target(const char *raw, const char *token) {
    if (app.window) {
        if (token && *token) gtk_window_set_startup_id(GTK_WINDOW(app.window), token);
        gtk_widget_show(app.window);
        gtk_window_deiconify(GTK_WINDOW(app.window));
        gtk_window_present(GTK_WINDOW(app.window));
        publish_visibility();
    }
    if (!raw || !*raw) return;
    const char *sep = strchr(raw, '\n');
    char *terminal_id = sep ? g_strndup(raw, (gsize)(sep - raw)) : g_strdup(raw);
    const char *connection_id = sep ? sep + 1 : "";
    if (*terminal_id) {
        char *q_term = quote(terminal_id);
        char *q_conn = quote(connection_id);
        char *code = g_strdup_printf("window.dispatchEvent(new CustomEvent('cage-notification-click',{detail:{terminal_id:%s,connection_id:%s}}));", q_term, q_conn);
        script(code);
        g_free(code); g_free(q_conn); g_free(q_term);
    }
    g_free(terminal_id);
}
static void notification_signal(GDBusConnection *connection, const char *sender, const char *path, const char *interface, const char *signal, GVariant *parameters, gpointer data) {
    (void)connection; (void)sender; (void)path; (void)interface; (void)data;
    guint32 id = 0;
    if (!strcmp(signal, "ActivationToken") && g_variant_is_of_type(parameters, G_VARIANT_TYPE("(us)"))) {
        const char *token = NULL;
        g_variant_get(parameters, "(u&s)", &id, &token);
        if (g_hash_table_contains(notify_targets, GUINT_TO_POINTER(id)))
            g_hash_table_replace(notify_tokens, GUINT_TO_POINTER(id), g_strdup(token));
    } else if (!strcmp(signal, "ActionInvoked") && g_variant_is_of_type(parameters, G_VARIANT_TYPE("(us)"))) {
        const char *action_key = NULL;
        g_variant_get(parameters, "(u&s)", &id, &action_key);
        char *target = g_hash_table_lookup(notify_targets, GUINT_TO_POINTER(id));
        if (target && !strcmp(action_key, "default"))
            open_notification_target(target, g_hash_table_lookup(notify_tokens, GUINT_TO_POINTER(id)));
        if (target) {
            g_hash_table_remove(notify_tokens, GUINT_TO_POINTER(id));
            g_hash_table_remove(notify_targets, GUINT_TO_POINTER(id));
        }
    } else if (!strcmp(signal, "NotificationClosed") && g_variant_is_of_type(parameters, G_VARIANT_TYPE("(uu)"))) {
        guint32 reason = 0;
        g_variant_get(parameters, "(uu)", &id, &reason);
        g_hash_table_remove(notify_tokens, GUINT_TO_POINTER(id));
        g_hash_table_remove(notify_targets, GUINT_TO_POINTER(id));
    }
}
static GDBusConnection *notification_bus(void) {
    if (notify_bus) return notify_bus;
    GError *error = NULL;
    notify_bus = g_bus_get_sync(G_BUS_TYPE_SESSION, NULL, &error);
    if (!notify_bus) {
        diagnostic_event(error ? error->message : "session bus unavailable");
        g_clear_error(&error);
        return NULL;
    }
    notify_targets = g_hash_table_new_full(g_direct_hash, g_direct_equal, NULL, g_free);
    notify_tokens = g_hash_table_new_full(g_direct_hash, g_direct_equal, NULL, g_free);
    g_dbus_connection_signal_subscribe(notify_bus, NOTIFY_NAME, NOTIFY_NAME, NULL, NOTIFY_PATH, NULL,
        G_DBUS_SIGNAL_FLAGS_NONE, notification_signal, NULL, NULL);
    return notify_bus;
}
/* The service reports whether it can play sounds; Cage offers the sound
   switch only when it can. An unreachable service is reported, not hidden. */
static void notification_status_finished(GObject *source, GAsyncResult *result, gpointer data) {
    PendingReply *pending = data;
    GError *error = NULL;
    GVariant *value = g_dbus_connection_call_finish(G_DBUS_CONNECTION(source), result, &error);
    gboolean sound = FALSE;
    if (value) {
        GVariantIter *caps = NULL;
        const char *cap = NULL;
        g_variant_get(value, "(as)", &caps);
        while (g_variant_iter_next(caps, "&s", &cap)) if (!strcmp(cap, "sound")) sound = TRUE;
        g_variant_iter_free(caps);
        g_variant_unref(value);
        reply_json(pending->reply, pending->context, sound
            ? "{\"supported\":true,\"permission\":\"authorized\",\"canOpenSettings\":false,\"sound\":true}"
            : "{\"supported\":true,\"permission\":\"authorized\",\"canOpenSettings\":false,\"sound\":false}");
    } else {
        diagnostic_event(error->message);
        g_clear_error(&error);
        reply_json(pending->reply, pending->context, "{\"supported\":false,\"permission\":\"unavailable\",\"canOpenSettings\":false,\"sound\":false}");
    }
    pending_free(pending);
}
static void notification_status(WebKitScriptMessageReply *reply, JSCContext *context) {
    GDBusConnection *bus = notification_bus();
    if (!bus) {
        reply_json(reply, context, "{\"supported\":false,\"permission\":\"unavailable\",\"canOpenSettings\":false,\"sound\":false}");
        return;
    }
    g_dbus_connection_call(bus, NOTIFY_NAME, NOTIFY_PATH, NOTIFY_NAME, "GetCapabilities", NULL, G_VARIANT_TYPE("(as)"),
        G_DBUS_CALL_FLAGS_NONE, 3000, NULL, notification_status_finished, pending_reply(reply, context));
}
typedef struct { PendingReply *pending; char *target; } NotificationSend;
static void notification_sent(GObject *source, GAsyncResult *result, gpointer data) {
    NotificationSend *send = data;
    GError *error = NULL;
    GVariant *value = g_dbus_connection_call_finish(G_DBUS_CONNECTION(source), result, &error);
    if (value) {
        guint32 id = 0;
        g_variant_get(value, "(u)", &id);
        g_variant_unref(value);
        if (g_hash_table_size(notify_targets) >= NOTIFY_TARGETS) {
            g_hash_table_remove_all(notify_targets);
            g_hash_table_remove_all(notify_tokens);
        }
        g_hash_table_replace(notify_targets, GUINT_TO_POINTER(id), send->target);
        send->target = NULL;
        reply_json(send->pending->reply, send->pending->context, "{\"delivered\":true,\"permission\":\"authorized\"}");
    } else {
        diagnostic_event(error->message);
        g_clear_error(&error);
        reply_json(send->pending->reply, send->pending->context, "{\"delivered\":false,\"permission\":\"unavailable\"}");
    }
    pending_free(send->pending);
    g_free(send->target);
    g_free(send);
}
static void notification_send(WebKitScriptMessageReply *reply, JSCContext *context, const char *title, const char *body,
                              const char *terminal_id, const char *connection_id, gboolean sound) {
    GDBusConnection *bus = notification_bus();
    if (!bus) {
        reply_json(reply, context, "{\"delivered\":false,\"permission\":\"unavailable\"}");
        return;
    }
    GVariantBuilder actions, hints;
    g_variant_builder_init(&actions, G_VARIANT_TYPE("as"));
    g_variant_builder_add(&actions, "s", "default");
    g_variant_builder_add(&actions, "s", "Cage");
    g_variant_builder_init(&hints, G_VARIANT_TYPE("a{sv}"));
    g_variant_builder_add(&hints, "{sv}", "desktop-entry", g_variant_new_string("io.github.lee_xeo.Cage"));
    if (sound) g_variant_builder_add(&hints, "{sv}", "sound-name", g_variant_new_string("message-new-instant"));
    else g_variant_builder_add(&hints, "{sv}", "suppress-sound", g_variant_new_boolean(TRUE));
    NotificationSend *send = g_new0(NotificationSend, 1);
    send->pending = pending_reply(reply, context);
    send->target = g_strdup_printf("%s\n%s", terminal_id, connection_id);
    g_dbus_connection_call(bus, NOTIFY_NAME, NOTIFY_PATH, NOTIFY_NAME, "Notify",
        g_variant_new("(susssasa{sv}i)", "Cage", 0, "io.github.lee_xeo.Cage", title, body, &actions, &hints, -1),
        G_VARIANT_TYPE("(u)"), G_DBUS_CALL_FLAGS_NONE, 5000, NULL, notification_sent, send);
}
static void language_refreshed(JsonObject *object, const GError *error, gpointer data) {
    PendingReply *pending = data;
    if (error) reply_error(pending->reply, error->message);
    else {
        set_messages(object);
        update_menu();
        gtk_button_set_label(GTK_BUTTON(app.retry), ui_text("重试连接"));
        gtk_button_set_label(GTK_BUTTON(app.existing), ui_text("打开现有环境保存文件"));
        gtk_button_set_label(GTK_BUTTON(app.complete), ui_text("保存后停止环境并完成更新"));
        gtk_window_set_title(GTK_WINDOW(app.window), app.needs_update ? ui_text("Cage（更新待完成）") : "Cage");
        reply_json(pending->reply, pending->context, "true");
    }
    pending_free(pending);
}
// Linux packages are installed by the system. The App only learns whether a
// newer package was published, so the workbench can point to it.
static char *update_latest;
static gboolean update_checking;
typedef struct { GSubprocess *process; GCancellable *cancel; guint deadline; PendingReply *pending; } UpdateCheck;
static void reply_update_state(WebKitScriptMessageReply *reply, JSCContext *context) {
    const char *text = CAGE_CLIENT_ONLY
        ? "轻量客户端使用独立安装包更新。退出客户端后更换程序；连接配置会保留，云端任务继续运行。"
        : "Linux 版本使用系统安装包更新。请保存工作、停止环境并退出 Cage，再安装新版 .deb；用户数据会保留。";
    char *version = quote(CAGE_VERSION), *message = quote(text), *latest = update_latest ? quote(update_latest) : NULL;
    char *state = latest
        ? g_strdup_printf("{\"phase\":\"manual\",\"currentVersion\":%s,\"latestVersion\":%s,\"message\":%s}", version, latest, message)
        : g_strdup_printf("{\"phase\":\"manual\",\"currentVersion\":%s,\"message\":%s}", version, message);
    reply_json(reply, context, state);
    g_free(state); g_free(version); g_free(message); g_free(latest);
}
static gboolean update_check_timeout(gpointer data) {
    UpdateCheck *check = data;
    check->deadline = 0;
    g_cancellable_cancel(check->cancel);
    g_subprocess_force_exit(check->process);
    return G_SOURCE_REMOVE;
}
static void update_check_finished(GObject *source, GAsyncResult *result, gpointer data) {
    UpdateCheck *check = data;
    char *output = NULL;
    if (check->deadline) g_source_remove(check->deadline);
    // The backend writes one state per line; the last line is the result. A
    // failed or unreadable check keeps what an earlier check found.
    if (g_subprocess_communicate_utf8_finish(G_SUBPROCESS(source), result, &output, NULL, NULL) && output && strlen(output) <= 65536) {
        g_strchomp(output);
        const char *line = strrchr(output, '\n');
        line = line ? line + 1 : output;
        JsonParser *parser = json_parser_new();
        if (json_parser_load_from_data(parser, line, -1, NULL) && JSON_NODE_HOLDS_OBJECT(json_parser_get_root(parser))) {
            JsonObject *state = json_node_get_object(json_parser_get_root(parser));
            const char *phase = text_member(state, "phase"), *latest = text_member(state, "latestVersion");
            gsize length = strlen(latest);
            if (!strcmp(phase, "available") && length > 0 && length <= 64 &&
                strspn(latest, "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ.-") == length) {
                g_free(update_latest);
                update_latest = g_strdup(latest);
            } else if (!strcmp(phase, "current")) g_clear_pointer(&update_latest, g_free);
        }
        g_object_unref(parser);
    }
    update_checking = FALSE;
    reply_update_state(check->pending->reply, check->pending->context);
    pending_free(check->pending);
    g_free(output);
    g_object_unref(check->process);
    g_object_unref(check->cancel);
    g_free(check);
}
static void update_check(WebKitScriptMessageReply *reply, JSCContext *context) {
    const char *args[] = {app.backend, "app-update", "check", NULL};
    GSubprocessLauncher *launcher = g_subprocess_launcher_new(G_SUBPROCESS_FLAGS_STDOUT_PIPE | G_SUBPROCESS_FLAGS_STDERR_SILENCE);
    sanitize_child_media_env(launcher);
    g_subprocess_launcher_setenv(launcher, "CLAUDE_CAGE_CONFIG", app.profile, TRUE);
    GSubprocess *process = update_checking ? NULL : g_subprocess_launcher_spawnv(launcher, args, NULL);
    g_object_unref(launcher);
    if (!process) { reply_update_state(reply, context); return; }
    UpdateCheck *check = g_new0(UpdateCheck, 1);
    check->process = process;
    check->cancel = g_cancellable_new();
    check->pending = pending_reply(reply, context);
    check->deadline = g_timeout_add_seconds(150, update_check_timeout, check);
    update_checking = TRUE;
    g_subprocess_communicate_utf8_async(process, NULL, check->cancel, update_check_finished, check);
}
static void browser_file_finished(JsonObject *object, const GError *error, gpointer data) {
    PendingReply *pending = data;
    if (error) reply_error(pending->reply, error->message);
    else {
        if (pending->sync) {
            json_object_set_int_member(object, "revision", pending->revision);
            const char *id = text_member(object, "files");
            if (*id && (pending->generation != clipboard_generation || pending->revision != clipboard_current_revision())) {
                const char *args[] = {app.backend, "clipboard-files", "--json", "--connection", pending->connection, "release", id, NULL};
                backend_arguments(args, 10, clipboard_cache_ignored, NULL);
            }
        }
        JsonNode *node = json_node_new(JSON_NODE_OBJECT);
        json_node_set_object(node, object);
        char *json = json_to_string(node, FALSE);
        reply_json(pending->reply, pending->context, json);
        g_free(json); json_node_free(node);
    }
    pending_free(pending);
}
static void clipboard_received(GtkClipboard *clipboard, const gchar *text, gpointer data) {
    (void)clipboard;
    PendingReply *pending = data;
    if (text && strlen(text) > MAX_MESSAGE) reply_error(pending->reply, "剪贴板内容超过 2 MB。");
    else reply_string(pending->reply, pending->context, text ? text : "");
    pending_free(pending);
}
static void clipboard_content_text(GtkClipboard *clipboard, const gchar *text, gpointer data) {
    (void)clipboard;
    PendingReply *pending = data;
    if (text && strlen(text) > MAX_MESSAGE) reply_error(pending->reply, "剪贴板内容超过 2 MB。");
    else {
        char *quoted = quote(text ? text : "");
        char *json = pending->sync ? g_strdup_printf("{\"text\":%s,\"revision\":%lu}", quoted, (unsigned long)pending->revision) : g_strdup_printf("{\"text\":%s}", quoted);
        reply_json(pending->reply, pending->context, json);
        g_free(json); g_free(quoted);
    }
    pending_free(pending);
}
static void clipboard_content_image(GtkClipboard *clipboard, GdkPixbuf *image, gpointer data) {
    PendingReply *pending = data;
    if (!image) {
        gtk_clipboard_request_text(clipboard, clipboard_content_text, pending);
        return;
    }
    gchar *buffer = NULL;
    gsize length = 0;
    if ((gint64)gdk_pixbuf_get_width(image) * gdk_pixbuf_get_height(image) > 40000000 ||
        !gdk_pixbuf_save_to_buffer(image, &buffer, &length, "png", NULL, NULL) || length > 20 * 1024 * 1024) {
        reply_error(pending->reply, "图片不能超过 20 MB 或 4000 万像素。");
    } else {
        char *encoded = g_base64_encode((const guchar *)buffer, length);
        char *json = pending->sync ? g_strdup_printf("{\"png\":\"%s\",\"revision\":%lu}", encoded, (unsigned long)pending->revision) : g_strdup_printf("{\"png\":\"%s\"}", encoded);
        reply_json(pending->reply, pending->context, json);
        g_free(json); g_free(encoded);
    }
    g_free(buffer);
    pending_free(pending);
}
static void clipboard_rich_text(GtkClipboard *clipboard, const gchar *text, gpointer data) {
    if (text && *text) clipboard_content_text(clipboard, text, data);
    else gtk_clipboard_request_image(clipboard, clipboard_content_image, data);
}
/* Word processors and spreadsheets publish a rendered picture next to the
   text they copied. Such a copy is text: sharing the picture would replace
   the text with an image on both sides. */
static void clipboard_content_targets(GtkClipboard *clipboard, GdkAtom *targets, gint count, gpointer data) {
    gboolean rich = FALSE;
    for (gint index = 0; targets && index < count && !rich; index++) {
        char *name = gdk_atom_name(targets[index]);
        rich = name && (!strcmp(name, "text/rtf") || !strcmp(name, "application/rtf") || !strcmp(name, "text/richtext"));
        g_free(name);
    }
    if (rich && gtk_targets_include_text(targets, count)) gtk_clipboard_request_text(clipboard, clipboard_rich_text, data);
    else gtk_clipboard_request_image(clipboard, clipboard_content_image, data);
}
static void file_selection_free(gpointer data) {
    FileSelection *selection = data;
    g_ptr_array_unref(selection->paths);
    g_free(selection);
}
static char *remember_files(GPtrArray *paths, GError **error) {
    if (!paths->len || paths->len > 100) {
        g_set_error_literal(error, G_IO_ERROR, G_IO_ERROR_INVALID_ARGUMENT, "请选择 1 至 100 个文件。");
        return NULL;
    }
    for (guint i = 0; i < paths->len; i++) {
        const char *path = g_ptr_array_index(paths, i);
        GStatBuf info;
        if (!g_path_is_absolute(path) || g_lstat(path, &info) != 0 || !S_ISREG(info.st_mode)) {
            g_set_error_literal(error, G_IO_ERROR, G_IO_ERROR_INVALID_ARGUMENT, "请选择本机普通文件，暂不支持文件夹或链接。");
            return NULL;
        }
        if (info.st_size > 2LL * 1024 * 1024 * 1024) {
            g_set_error_literal(error, G_IO_ERROR, G_IO_ERROR_INVALID_ARGUMENT, "每个文件不能超过 2 GB。");
            return NULL;
        }
    }
    if (!file_selections) file_selections = g_hash_table_new_full(g_str_hash, g_str_equal, g_free, file_selection_free);
    gint64 now = g_get_monotonic_time();
    GHashTableIter iter; gpointer key, value;
    g_hash_table_iter_init(&iter, file_selections);
    while (g_hash_table_iter_next(&iter, &key, &value))
        if (((FileSelection *)value)->expires <= now) g_hash_table_iter_remove(&iter);
    if (g_hash_table_size(file_selections) >= 16) {
        gpointer oldest_key = NULL; gint64 oldest_time = G_MAXINT64;
        g_hash_table_iter_init(&iter, file_selections);
        while (g_hash_table_iter_next(&iter, &key, &value)) {
            if (((FileSelection *)value)->expires < oldest_time) { oldest_key = key; oldest_time = ((FileSelection *)value)->expires; }
        }
        g_hash_table_remove(file_selections, oldest_key);
    }
    FileSelection *selection = g_new0(FileSelection, 1);
    selection->paths = g_ptr_array_ref(paths); selection->expires = now + 600 * G_USEC_PER_SEC;
    char *id = g_uuid_string_random();
    g_hash_table_insert(file_selections, id, selection);
    return g_strdup_printf("{\"fileSelection\":\"%s\",\"count\":%u}", id, paths->len);
}
static void reply_selected_uris(char **uris, PendingReply *pending) {
    GPtrArray *paths = g_ptr_array_new_with_free_func(g_free);
    GError *error = NULL;
    for (guint i = 0; uris[i]; i++) {
        char *hostname = NULL;
        char *path = g_filename_from_uri(uris[i], &hostname, NULL);
        if (!path || (hostname && *hostname && strcmp(hostname, "localhost"))) {
            g_free(path); g_free(hostname);
            g_set_error_literal(&error, G_IO_ERROR, G_IO_ERROR_INVALID_ARGUMENT, "请选择本机文件。");
            break;
        }
        g_free(hostname); g_ptr_array_add(paths, path);
    }
    if (!error && pending->sync && !pending->defer_files && pending->generation != clipboard_generation)
        g_set_error_literal(&error, G_IO_ERROR, G_IO_ERROR_CANCELLED, "剪贴板文件传递已取消。");
    if (!error && pending->sync) {
        if (pending->defer_files) {
            char *json = g_strdup_printf("{\"revision\":%lu,\"files_pending\":true,\"count\":%u}", (unsigned long)pending->revision, paths->len);
            reply_json(pending->reply, pending->context, json);
            g_free(json); g_ptr_array_unref(paths); pending_free(pending); return;
        }
        if (!clipboard_readers) clipboard_readers = g_ptr_array_new();
        pending->clipboard_paths = g_ptr_array_ref(paths);
        g_ptr_array_add(clipboard_readers, pending->clipboard_paths);
        const char **args = g_new0(const char *, paths->len + 7);
        args[0] = app.backend; args[1] = "clipboard-files"; args[2] = "--json"; args[3] = "--connection"; args[4] = pending->connection; args[5] = "send";
        for (guint i = 0; i < paths->len; i++) args[i + 6] = g_ptr_array_index(paths, i);
        Command *command = backend_arguments(args, 1800, browser_file_finished, pending);
        if (command) {
            if (!clipboard_sends) clipboard_sends = g_ptr_array_new();
            g_ptr_array_add(clipboard_sends, command);
        }
        g_free(args); g_ptr_array_unref(paths); return;
    }
    char *json = error ? NULL : remember_files(paths, &error);
    if (error) reply_error(pending->reply, error->message);
    else reply_json(pending->reply, pending->context, json);
    g_free(json); g_clear_error(&error); g_ptr_array_unref(paths); pending_free(pending);
}
static void clipboard_gnome_files(GtkClipboard *clipboard, GtkSelectionData *selection, gpointer data) {
    gint length = gtk_selection_data_get_length(selection);
    if (length <= 0) { gtk_clipboard_request_targets(clipboard, clipboard_content_targets, data); return; }
    if (length > MAX_MESSAGE) {
        PendingReply *pending = data; reply_error(pending->reply, "剪贴板文件列表过大。"); pending_free(pending); return;
    }
    char *text = g_strndup((const char *)gtk_selection_data_get_data(selection), length);
    char *start = strchr(text, '\n');
    char **uris = start ? g_uri_list_extract_uris(start + 1) : NULL;
    if (uris && uris[0]) reply_selected_uris(uris, data);
    else { PendingReply *pending = data; reply_error(pending->reply, "剪贴板文件列表无效，请重新复制文件。"); pending_free(pending); }
    g_strfreev(uris); g_free(text);
}
static void clipboard_file_uris(GtkClipboard *clipboard, gchar **uris, gpointer data) {
    if (uris && uris[0]) reply_selected_uris(uris, data);
    else gtk_clipboard_request_contents(clipboard, gdk_atom_intern_static_string("x-special/gnome-copied-files"), clipboard_gnome_files, data);
}
static void clipboard_sync_reply(PendingReply *pending, gboolean superseded) {
    gdk_display_sync(gdk_display_get_default());
    while (g_main_context_iteration(NULL, FALSE)) { }
    char *json = g_strdup_printf("{\"revision\":%lu,\"superseded\":%s}", (unsigned long)clipboard_current_revision(), superseded ? "true" : "false");
    reply_json(pending->reply, pending->context, json); g_free(json); pending_free(pending);
}
static void clipboard_cache_command(const char *action, const char *id, BackendCallback callback, gpointer data) {
    char *quoted = quote(id ? id : "");
    char *request = g_strdup_printf("{\"action\":\"%s\",\"cache_id\":%s}", action, quoted);
    const char *args[] = {app.backend, "clipboard-files", "--json", "cache", request, NULL};
    backend_arguments_limited(args, 10, 30 << 20, callback, data);
    g_free(request); g_free(quoted);
}
static void clipboard_cache_ignored(JsonObject *object, const GError *error, gpointer data) { (void)object; (void)error; (void)data; }
static void clipboard_copy_free(gpointer data) { ClipboardCopy *copy = data; g_ptr_array_unref(copy->paths); g_free(copy); }
static void clipboard_cache_remember(JsonObject *object, gboolean pending) {
    const char *id = text_member(object, "cache_id");
    JsonArray *paths = json_object_has_member(object, "paths") ? json_object_get_array_member(object, "paths") : NULL;
    if (!*id || !paths || !json_array_get_length(paths) || g_hash_table_contains(clipboard_copies, id)) return;
    ClipboardCopy *copy = g_new0(ClipboardCopy, 1);
    copy->pending = pending;
    copy->paths = g_ptr_array_new_with_free_func(g_free);
    for (guint i = 0; i < json_array_get_length(paths); i++) g_ptr_array_add(copy->paths, g_strdup(json_array_get_string_element(paths, i)));
    g_hash_table_insert(clipboard_copies, g_strdup(id), copy);
}
typedef struct { char *id; PendingReply *discarded; } ClipboardDisposal;
static void clipboard_cache_released(JsonObject *object, const GError *error, gpointer data) {
    (void)object;
    ClipboardDisposal *disposal = data;
    ClipboardCopy *copy = g_hash_table_lookup(clipboard_copies, disposal->id);
    if (copy) { copy->releasing = FALSE; if (!error) g_hash_table_remove(clipboard_copies, disposal->id); }
    if (disposal->discarded) clipboard_sync_reply(disposal->discarded, TRUE);
    g_free(disposal->id); g_free(disposal);
}
static void clipboard_cache_release(const char *id, PendingReply *discarded) {
    if (!id || !*id) { if (discarded) clipboard_sync_reply(discarded, TRUE); return; }
    ClipboardCopy *copy = g_hash_table_lookup(clipboard_copies, id);
    if (copy) { copy->releasing = TRUE; copy->pending = FALSE; }
    ClipboardDisposal *disposal = g_new0(ClipboardDisposal, 1);
    disposal->id = g_strdup(id); disposal->discarded = discarded;
    clipboard_cache_command("release", id, clipboard_cache_released, disposal);
}
static gboolean clipboard_cache_referenced(GPtrArray *selected, GPtrArray *owned) {
    if (!selected) return FALSE;
    char *root = g_path_get_dirname(g_ptr_array_index(owned, 0));
    char *prefix = g_strconcat(root, G_DIR_SEPARATOR_S, NULL);
    gboolean found = FALSE;
    for (guint i = 0; i < selected->len && !found; i++) {
        const char *path = g_ptr_array_index(selected, i);
        char *parent = g_strconcat(path, G_DIR_SEPARATOR_S, NULL);
        found = !strcmp(path, root) || g_str_has_prefix(path, prefix) || g_str_has_prefix(root, parent);
        g_free(parent);
    }
    g_free(root); g_free(prefix); return found;
}
static void clipboard_cache_collect_uris(char **uris, guint64 revision) {
    clipboard_cache_collecting = FALSE;
    if (revision != clipboard_revision) return;
    GPtrArray *selected = g_ptr_array_new_with_free_func(g_free);
    for (guint i = 0; uris && uris[i]; i++) {
        char *path = g_filename_from_uri(uris[i], NULL, NULL);
        if (!path) { g_ptr_array_unref(selected); return; }
        g_ptr_array_add(selected, path);
    }
    GHashTableIter iterator; gpointer key, value;
    g_hash_table_iter_init(&iterator, clipboard_copies);
    while (g_hash_table_iter_next(&iterator, &key, &value)) {
        ClipboardCopy *copy = value;
        gboolean referenced = copy->pending || copy->releasing || clipboard_cache_referenced(selected, copy->paths);
        for (guint i = 0; clipboard_readers && i < clipboard_readers->len && !referenced; i++)
            referenced = clipboard_cache_referenced(g_ptr_array_index(clipboard_readers, i), copy->paths);
        if (!referenced) clipboard_cache_release(key, NULL);
    }
    g_ptr_array_unref(selected);
}
static void clipboard_cache_gnome(GtkClipboard *clipboard, GtkSelectionData *selection, gpointer data) {
    (void)clipboard;
    guint64 revision = *(guint64 *)data;
    gint length = gtk_selection_data_get_length(selection);
    if (length > MAX_MESSAGE) { clipboard_cache_collecting = FALSE; g_free(data); return; }
    char *text = length > 0 ? g_strndup((const char *)gtk_selection_data_get_data(selection), length) : NULL;
    char *start = text ? strchr(text, '\n') : NULL;
    char **uris = start ? g_uri_list_extract_uris(start + 1) : NULL;
    clipboard_cache_collect_uris(uris, revision);
    g_strfreev(uris); g_free(text); g_free(data);
}
static void clipboard_cache_uris(GtkClipboard *clipboard, gchar **uris, gpointer data) {
    if (!uris) { gtk_clipboard_request_contents(clipboard, gdk_atom_intern_static_string("x-special/gnome-copied-files"), clipboard_cache_gnome, data); return; }
    clipboard_cache_collect_uris(uris, *(guint64 *)data); g_free(data);
}
static gboolean clipboard_cache_tick(gpointer data) {
    (void)data;
    if (!clipboard_cache_loaded || clipboard_cache_collecting || !g_hash_table_size(clipboard_copies)) return G_SOURCE_CONTINUE;
    clipboard_cache_collecting = TRUE;
    guint64 *revision = g_new(guint64, 1); *revision = clipboard_revision;
    gtk_clipboard_request_uris(gtk_clipboard_get(GDK_SELECTION_CLIPBOARD), clipboard_cache_uris, revision);
    return G_SOURCE_CONTINUE;
}
static void clipboard_cache_loaded_reply(JsonObject *object, const GError *error, gpointer data) {
    (void)data;
    clipboard_cache_loaded = TRUE;
    if (error || !object || !json_object_has_member(object, "selections")) return;
    JsonArray *items = json_object_get_array_member(object, "selections");
    for (guint i = 0; items && i < json_array_get_length(items); i++) clipboard_cache_remember(json_array_get_object_element(items, i), FALSE);
    clipboard_cache_tick(NULL);
}
static void clipboard_cache_watch(void) {
    if (clipboard_copies) return;
    clipboard_copies = g_hash_table_new_full(g_str_hash, g_str_equal, g_free, clipboard_copy_free);
    g_timeout_add_seconds(1, clipboard_cache_tick, NULL);
    clipboard_cache_command("list", "", clipboard_cache_loaded_reply, NULL);
}
static void clipboard_sync_provide(GtkClipboard *clipboard, GtkSelectionData *selection, guint info, gpointer data) {
    (void)clipboard; char **payload = data;
    gtk_selection_data_set(selection, gdk_atom_intern_static_string(info ? "x-special/gnome-copied-files" : "text/uri-list"),8,(const guchar *)payload[info],strlen(payload[info]));
}
static void clipboard_sync_clear(GtkClipboard *clipboard, gpointer data) { (void)clipboard; g_strfreev(data); }
static void clipboard_files_received(JsonObject *object, const GError *error, gpointer data) {
    PendingReply *pending = data;
    if (error) { clipboard_cache_command("list", "", clipboard_cache_loaded_reply, NULL); reply_error(pending->reply, error->message); pending_free(pending); return; }
    clipboard_cache_watch(); clipboard_cache_remember(object, TRUE);
    const char *cache = text_member(object, "cache_id");
    if (pending->generation != clipboard_generation || pending->revision != clipboard_current_revision()) { clipboard_cache_release(cache, pending); return; }
    JsonArray *paths = json_object_get_array_member(object, "paths");
    if (!paths || !json_array_get_length(paths)) { clipboard_cache_release(cache, NULL); reply_error(pending->reply,"剪贴板文件读取失败。"); pending_free(pending); return; }
    GString *text = g_string_new("copy\n"), *uris = g_string_new("");
    for (guint i = 0; i < json_array_get_length(paths); i++) {
        char *uri = g_filename_to_uri(json_array_get_string_element(paths, i), NULL, NULL);
        if (!uri) { clipboard_cache_release(cache, NULL); g_string_free(text,TRUE); g_string_free(uris,TRUE); reply_error(pending->reply,"剪贴板文件位置无效。"); pending_free(pending); return; }
        g_string_append_printf(text,"%s\n",uri); g_string_append_printf(uris,"%s\r\n",uri); g_free(uri);
    }
    /* GTK copies these values into an owned provider, including native file
       manager targets. Use the selection callbacks below for both formats. */
    GtkTargetEntry targets[] = {{"text/uri-list",0,0},{"x-special/gnome-copied-files",0,1}};
    char **payload = g_new0(char *,3); payload[0] = g_string_free(uris,FALSE); payload[1] = g_string_free(text,FALSE);
    if (!gtk_clipboard_set_with_data(gtk_clipboard_get(GDK_SELECTION_CLIPBOARD), targets,2, clipboard_sync_provide,clipboard_sync_clear,payload)) {
        clipboard_cache_release(cache, NULL); g_strfreev(payload); reply_error(pending->reply,"剪贴板文件写入失败。"); pending_free(pending);
    } else {
        clipboard_revision++;
        ClipboardCopy *copy = g_hash_table_lookup(clipboard_copies, cache); if (copy) copy->pending = FALSE;
        if (*cache) clipboard_cache_command("publish", cache, clipboard_cache_ignored, NULL);
        clipboard_sync_reply(pending,FALSE);
    }
}
static void session_refreshed(JsonObject *object, const GError *error, gpointer data) {
    PendingReply *pending = data;
    const char *url = text_member(object, "url");
    if (error || !valid_workbench(url, 0, TRUE))
        reply_error(pending->reply, error ? error->message : "工作台会话恢复失败，请重新连接。");
    else {
        char *quoted = quote(url);
        char *json = g_strdup_printf("{\"url\":%s}", quoted);
        reply_json(pending->reply, pending->context, json);
        g_free(json); g_free(quoted);
    }
    pending_free(pending);
}
static void smoke_report(char *evidence) {
    GError *error = NULL;
    if (g_file_set_contents(app.smoke_output, evidence, -1, &error)) app.result = 0;
    else {
        g_printerr("Linux smoke: cannot save report: %s\n", error->message);
        g_clear_error(&error);
    }
    g_free(evidence);
    g_application_quit(G_APPLICATION(app.application));
}
static void smoke_stopped(JsonObject *object, const GError *error, gpointer data) {
    const char *status = text_member(object, "status");
    if (error || g_strcmp0(status, "prepared") != 0) {
        /* prepare-update returns only status and message, never a session URL. */
        const char *message = text_member(object, "message");
        g_printerr("Linux smoke: controller cleanup failed (error: %.500s; status: %s; message: %.500s).\n",
            error ? error->message : "none", *status ? status : "none", *message ? message : "none");
        g_free(data);
        g_application_quit(G_APPLICATION(app.application));
    } else smoke_report(data);
}
/* The page can still ask the bridge to refresh its session, which runs
 * "desktop" and may start a new connection service while prepare-update is
 * retiring the old one. Refuse new bridge requests, then let commands already
 * running finish before the smoke reports or stops the services. */
static gboolean smoke_finish_when_idle(gpointer data) {
    if (app.backend_commands) return G_SOURCE_CONTINUE;
    if (g_strcmp0(app.storage_phase, "seed") == 0) smoke_report(data);
    else backend("prepare-update", NULL, smoke_stopped, data);
    return G_SOURCE_REMOVE;
}
static void smoke_snapshot(GObject *source, GAsyncResult *result, gpointer data) {
    char *report = data;
    GError *error = NULL;
    cairo_surface_t *surface = webkit_web_view_get_snapshot_finish(WEBKIT_WEB_VIEW(source), result, &error);
    char *path = g_strconcat(app.smoke_output, ".png", NULL);
    gboolean saved = surface && cairo_surface_write_to_png(surface, path) == CAIRO_STATUS_SUCCESS;
    if (surface) cairo_surface_destroy(surface);
    g_free(path); g_clear_error(&error);
    if (saved) {
        app.smoke_finishing = TRUE;
        if (smoke_finish_when_idle(report) == G_SOURCE_CONTINUE) g_timeout_add(50, smoke_finish_when_idle, report);
    } else {
        g_printerr("Linux smoke: could not capture rendered workbench.\n");
        g_free(report);
        g_application_quit(G_APPLICATION(app.application));
    }
}
static void smoke_evaluated(GObject *source, GAsyncResult *result, gpointer data) {
    (void)data;
    GError *error = NULL;
    JSCValue *value = webkit_web_view_call_async_javascript_function_finish(WEBKIT_WEB_VIEW(source), result, &error);
    if (!value || error || !jsc_value_is_object(value)) {
        g_printerr("Linux smoke: authenticated workbench verification failed (error domain=%s, code=%d, object=%d).\n",
            error ? g_quark_to_string(error->domain) : "none", error ? error->code : 0,
            value ? jsc_value_is_object(value) : FALSE);
        g_clear_error(&error); g_clear_object(&value);
        g_application_quit(G_APPLICATION(app.application));
        return;
    }
    char *json = jsc_value_to_json(value, 0);
    JsonParser *parser = json_parser_new();
    gboolean ok = json && json_parser_load_from_data(parser, json, -1, NULL) &&
        JSON_NODE_HOLDS_OBJECT(json_parser_get_root(parser));
    JsonObject *object = ok ? json_node_get_object(json_parser_get_root(parser)) : NULL;
    ok = object && json_object_has_member(object, "success") && json_object_get_boolean_member(object, "success");
    if (ok) {
        json_object_set_string_member(object, "version", CAGE_VERSION);
        json_object_set_int_member(object, "webkitMajor", webkit_get_major_version());
        json_object_set_int_member(object, "webkitMinor", webkit_get_minor_version());
        json_object_set_int_member(object, "webkitMicro", webkit_get_micro_version());
        char *report = json_text(json_parser_get_root(parser));
        webkit_web_view_get_snapshot(app.web, WEBKIT_SNAPSHOT_REGION_VISIBLE, WEBKIT_SNAPSHOT_OPTIONS_NONE,
            NULL, smoke_snapshot, report);
    } else {
        g_printerr("Linux smoke: workbench evidence was incomplete: %s\n", json ? json : "null");
        g_application_quit(G_APPLICATION(app.application));
    }
    g_free(json); g_object_unref(parser); g_object_unref(value);
}
static gboolean start_smoke(gpointer data) {
    (void)data;
    char *phase = quote(app.storage_phase ? app.storage_phase : "");
    char *marker = quote(app.storage_marker ? app.storage_marker : "");
    char *body = g_strdup_printf(
        "let stage='storage';try{"
        "const phase=%s,marker=%s;const storageKey='cage-linux-smoke-persistence';"
        "if(phase==='seed')localStorage.setItem(storageKey,marker);"
        "const storageMarkerMatched=!phase||localStorage.getItem(storageKey)===marker;"
        "if(phase==='verify'&&storageMarkerMatched)localStorage.removeItem(storageKey);"
        "stage='bridge';const bridge=window.cageNativeBridge;"
        "let unauthenticatedBridgeDenied=false;try{await window.webkit.messageHandlers.cageTransport.postMessage({token:'invalid',request:{action:'clipboardRead'}});}catch{unauthenticatedBridgeDenied=true;}"
        "stage='account-device';const native=await bridge.postMessage({action:'accountDevice',operation:'status'});"
        "stage='app-update';const update=await bridge.postMessage({action:'appUpdate',operation:'status'});"
        "stage='context';const context=await (await fetch('/api/client/context')).json();"
        "if(context.desktop_gateway&&context.mode!=='local')throw new Error('local workbench not selected');"
        "const scoped={headers:{'X-Cage-Connection':context.connection_id}};"
        "stage='account';const accountResponse=await fetch('/api/account',scoped);const account=await accountResponse.json();"
        "stage='status';const stateResponse=await fetch('/api/status',scoped);const state=await stateResponse.json();"
        "stage='settings';const settingsResponse=await fetch('/api/settings',scoped);await settingsResponse.json();"
        "const root=document.getElementById('root');"
        "const rendered=!!root&&root.innerText.trim().length>40&&root.querySelectorAll('button').length>3;"
        "const success=rendered&&accountResponse.ok&&account.unlocked===true&&stateResponse.ok&&settingsResponse.ok&&native.supported===false&&update.phase==='manual'&&unauthenticatedBridgeDenied&&storageMarkerMatched;"
        "return {success,nativeBridgeReady:true,authenticated:accountResponse.ok,accountUnlocked:account.unlocked===true,"
        "workbenchRendered:rendered,workbenchReadyAcknowledged:true,statusReceived:stateResponse.ok,settingsReceived:settingsResponse.ok,unauthenticatedBridgeDenied,"
        "storageMarkerMatched,localStoragePersisted:phase==='verify'&&storageMarkerMatched};"
        "}catch(error){return {success:false,stage,errorName:error.name};}", phase, marker);
    webkit_web_view_call_async_javascript_function(app.web, body, -1, NULL, NULL, NULL, NULL, smoke_evaluated, NULL);
    g_free(body); g_free(phase); g_free(marker);
    return G_SOURCE_REMOVE;
}
static gboolean bridge_received(WebKitUserContentManager *manager, JSCValue *value, WebKitScriptMessageReply *reply, gpointer data) {
    (void)manager; (void)data;
    char *json = jsc_value_to_json(value, 0);
    JsonParser *parser = json_parser_new();
    if (!json || strlen(json) > 30 * 1024 * 1024 || !json_parser_load_from_data(parser, json, -1, NULL) ||
        !JSON_NODE_HOLDS_OBJECT(json_parser_get_root(parser))) {
        reply_error(reply, "原生请求格式无效。");
        goto done;
    }
    JsonObject *envelope = json_node_get_object(json_parser_get_root(parser));
    if (!app.token || g_strcmp0(text_member(envelope, "token"), app.token) != 0 ||
        !valid_workbench(webkit_web_view_get_uri(app.web), app.port, FALSE)) {
        reply_error(reply, "请求来源无效。");
        goto done;
    }
    JsonNode *request_node = json_object_get_member(envelope, "request");
    if (!request_node || !JSON_NODE_HOLDS_OBJECT(request_node)) {
        reply_error(reply, "原生请求格式无效。");
        goto done;
    }
    if (app.smoke_finishing) {
        reply_error(reply, "自检正在结束，未执行新的原生操作。");
        goto done;
    }
    JsonObject *request = json_node_get_object(request_node);
    const char *name = text_member(request, "action");
    JSCContext *context = jsc_value_get_context(value);
    if (!strcmp(name, "remoteInput")) {
        reply_json(reply, context, input_handle(request) ? "true" : "false");
    } else if (!strcmp(name, "exportDiagnostics")) {
        if (app.picker_open) reply_error(reply, "请先完成当前文件窗口。");
        else {
            char *path = choose_file(GTK_FILE_CHOOSER_ACTION_SAVE, "导出诊断包", "cage-diagnostics.zip");
            if (!path) reply_json(reply, context, "{\"canceled\":true}");
            else {
                const char *connection = text_member(request, "connection_id");
                const char *args[] = {app.backend, "diagnostics", "--json", "--connection", *connection ? connection : "local", path, NULL};
                backend_arguments(args, 60, browser_file_finished, pending_reply(reply, context));
                g_free(path);
            }
        }
    } else if (!strcmp(name, "workbenchReady")) {
        if (app.needs_update) reply_error(reply, "后台版本尚未同步，更新未完成。");
        else {
            app.ready = TRUE;
            reply_json(reply, context, "true");
            if (app.smoke_output && !app.smoke_started) {
                app.smoke_started = TRUE;
                g_idle_add(start_smoke, NULL);
            }
        }
    } else if (!strcmp(name, "accountDevice")) {
        if (!strcmp(text_member(request, "operation"), "status")) reply_json(reply, context, "{\"supported\":false}");
        else reply_error(reply, "Linux 版本使用 Cage 账户密码解锁。");
    } else if (!strcmp(name, "appReference")) {
        const char *kind = text_member(request, "kind");
        const char *uri = !strcmp(kind, "source") ? "https://github.com/Lee-xeo/Claude-Cage/commits/main"
            : !strcmp(kind, "releases") ? "https://github.com/Lee-xeo/Claude-Cage/releases" : NULL;
        if (!uri) reply_error(reply, "未知的项目页面");
        else {
            GError *error = NULL;
            GAppLaunchContext *launch_context = g_app_launch_context_new();
            if (app.media_plugins_isolated) {
                g_app_launch_context_unsetenv(launch_context, "GST_PLUGIN_SYSTEM_PATH_1_0");
                g_app_launch_context_unsetenv(launch_context, "GST_REGISTRY_1_0");
            }
            if (app.dmabuf_default_set)
                g_app_launch_context_unsetenv(launch_context, "WEBKIT_DISABLE_DMABUF_RENDERER");
            if (g_app_info_launch_default_for_uri(uri, launch_context, &error)) reply_json(reply, context, "true");
            else { reply_error(reply, "无法打开项目页面，请检查默认浏览器"); g_clear_error(&error); }
            g_object_unref(launch_context);
        }
    } else if (!strcmp(name, "appUpdate")) {
        const char *operation = text_member(request, "operation");
        if (strcmp(operation, "status") && strcmp(operation, "check") && strcmp(operation, "install") && strcmp(operation, "cancel"))
            reply_error(reply, "更新操作无效。");
        else if (!strcmp(operation, "check") && !CAGE_CLIENT_ONLY) update_check(reply, context);
        else reply_update_state(reply, context);
    } else if (!strcmp(name, "desktopFileAction")) {
        const char *id = text_member(request, "id"), *connection = text_member(request, "connection_id");
        if (!g_uuid_string_is_valid(id)) reply_error(reply, "文件打开标识无效。");
        else {
            const char *args[] = {app.backend,"desktop-file","--json","--connection",*connection ? connection : "local",id,NULL};
            backend_arguments(args,1800,browser_file_finished,pending_reply(reply,context));
        }
    } else if (!strcmp(name, "saveBrowserDownload") || !strcmp(name, "browserDownloadAction")) {
        const char *id = text_member(request, "id");
        const char *operation = !strcmp(name, "saveBrowserDownload") ? "save" : text_member(request, "operation");
        if (!g_uuid_string_is_valid(id) || (strcmp(operation,"save") && strcmp(operation,"open") && strcmp(operation,"reveal"))) {
            reply_error(reply, "下载操作无效。");
        } else if (app.picker_open) {
            reply_error(reply, "请先完成当前文件窗口。");
        } else {
            char *path = NULL;
            if (!strcmp(operation,"save")) {
                app.picker_open = TRUE;
                GtkWidget *dialog = gtk_file_chooser_dialog_new(ui_text("将下载另存为到本机"), GTK_WINDOW(app.window), GTK_FILE_CHOOSER_ACTION_SAVE,
                    ui_text("取消"), GTK_RESPONSE_CANCEL, ui_text("保存"), GTK_RESPONSE_ACCEPT, NULL);
                char *base = g_path_get_basename(text_member(request,"name"));
                gtk_file_chooser_set_current_name(GTK_FILE_CHOOSER(dialog), *base ? base : "download");
                gtk_file_chooser_set_do_overwrite_confirmation(GTK_FILE_CHOOSER(dialog), TRUE);
                if (gtk_dialog_run(GTK_DIALOG(dialog)) == GTK_RESPONSE_ACCEPT) path = gtk_file_chooser_get_filename(GTK_FILE_CHOOSER(dialog));
                gtk_widget_destroy(dialog); g_free(base); app.picker_open = FALSE;
            }
            if (!strcmp(operation,"save") && !path) reply_json(reply,context,"{\"canceled\":true}");
            else {
                const char *connection = text_member(request, "connection_id");
                const char *args[] = {app.backend,"browser-file","--json","--connection",*connection ? connection : "local",operation,id,path,NULL};
                backend_arguments(args,1800,browser_file_finished,pending_reply(reply,context));
            }
            g_free(path);
        }
    } else if (!strcmp(name, "shareLocalProject")) {
        const char *connection = text_member(request, "connection_id"), *mode = text_member(request, "mode");
        if (app.picker_open || !*connection || (strcmp(mode,"read") && strcmp(mode,"write"))) {
            reply_error(reply, "请在云端工作台重新选择项目。");
        } else {
            const char *prompt = !strcmp(mode,"write") ? "云端 Cage 可以读取、修改和删除所选项目中的文件。" : "云端 Cage 可以读取所选项目中的文件。";
            char *path = choose_file(GTK_FILE_CHOOSER_ACTION_SELECT_FOLDER, prompt, NULL);
            if (path) {
                const char *args[] = {app.backend,"share-project","--json","--connection",connection,mode,path,NULL};
                backend_arguments(args,90,browser_file_finished,pending_reply(reply,context));
            } else reply_json(reply,context,"{\"canceled\":true}");
            g_free(path);
        }
    } else if (!strcmp(name, "chooseFolder")) {
        if (app.picker_open) reply_error(reply, "请先完成当前文件选择。");
        else {
            const char *prompt = !strcmp(text_member(request, "purpose"), "environmentStorage")
                ? "请选择用于保存镜像和虚拟磁盘的空文件夹。" : "仅此文件夹会与 Cage 共享。请选择具体项目。";
            char *path = choose_file(GTK_FILE_CHOOSER_ACTION_SELECT_FOLDER, prompt, NULL);
            reply_string(reply, context, path ? path : "");
            g_free(path);
        }
    } else if (!strcmp(name, "clipboardRead")) {
        gtk_clipboard_request_text(gtk_clipboard_get(GDK_SELECTION_CLIPBOARD), clipboard_received, pending_reply(reply, context));
    } else if (!strcmp(name, "clipboardRevision")) {
        char revision[32]; g_snprintf(revision,sizeof(revision),"%lu",(unsigned long)clipboard_current_revision()); reply_json(reply,context,revision);
    } else if (!strcmp(name, "clipboardSyncCancel")) {
        clipboard_generation++;
        for (guint i = 0; clipboard_sends && i < clipboard_sends->len; i++) {
            Command *command = g_ptr_array_index(clipboard_sends, i);
            g_subprocess_force_exit(command->process);
        }
        reply_json(reply,context,"true");
    } else if (!strcmp(name, "clipboardSyncRelease")) {
        const char *id = text_member(request, "files");
        const char *connection = text_member(request, "connection_id");
        if (!*connection) connection = "local";
        if (!g_uuid_string_is_valid(id)) reply_error(reply, "剪贴板文件标识无效。");
        else {
            const char *args[] = {app.backend, "clipboard-files", "--json", "--connection", connection, "release", id, NULL};
            backend_arguments(args, 10, browser_file_finished, pending_reply(reply, context));
        }
    } else if (!strcmp(name, "clipboardSyncRead")) {
        const char *connection = text_member(request, "connection_id");
        if (!*connection) connection = "local";
        PendingReply *pending = pending_reply(reply,context); pending->sync = TRUE; pending->generation = clipboard_generation; pending->revision = clipboard_current_revision(); pending->connection = g_strdup(connection);
        JsonNode *defer = json_object_get_member(request,"defer_files");
        pending->defer_files = defer && JSON_NODE_HOLDS_VALUE(defer) && json_node_get_value_type(defer) == G_TYPE_BOOLEAN && json_node_get_boolean(defer);
        gtk_clipboard_request_uris(gtk_clipboard_get(GDK_SELECTION_CLIPBOARD),clipboard_file_uris,pending);
    } else if (!strcmp(name, "clipboardSyncWrite")) {
        const char *connection = text_member(request, "connection_id");
        if (!*connection) connection = "local";
        PendingReply *pending = pending_reply(reply,context); pending->generation = clipboard_generation; pending->revision = json_object_has_member(request,"revision") ? (guint64)json_object_get_int_member(request,"revision") : clipboard_current_revision();
        if (pending->revision != clipboard_current_revision()) clipboard_sync_reply(pending,TRUE);
        else if (*text_member(request,"files")) {
            const char *id = text_member(request,"files");
            if (!g_uuid_string_is_valid(id)) { reply_error(reply,"剪贴板文件标识无效。"); pending_free(pending); }
            else { const char *args[] = {app.backend,"clipboard-files","--json","--connection",connection,"receive",id,NULL}; backend_arguments(args,1800,clipboard_files_received,pending); }
        } else if (*text_member(request,"png")) {
            gsize length = 0; guchar *bytes = g_base64_decode(text_member(request,"png"),&length);
            GdkPixbufLoader *loader = gdk_pixbuf_loader_new_with_type("png",NULL);
            gboolean valid = loader && length <= 20 * 1024 * 1024 && gdk_pixbuf_loader_write(loader,bytes,length,NULL) && gdk_pixbuf_loader_close(loader,NULL);
            GdkPixbuf *image = valid ? gdk_pixbuf_loader_get_pixbuf(loader) : NULL;
            if (!image || (gint64)gdk_pixbuf_get_width(image) * gdk_pixbuf_get_height(image) > 40000000) { reply_error(reply,"剪贴板图片格式或大小无效。"); pending_free(pending); }
            else { gtk_clipboard_set_image(gtk_clipboard_get(GDK_SELECTION_CLIPBOARD),image); clipboard_revision++; clipboard_sync_reply(pending,FALSE); }
            if (loader) g_object_unref(loader);
            g_free(bytes);
        } else if (json_object_has_member(request,"text") && strlen(text_member(request,"text")) <= MAX_MESSAGE) {
            gtk_clipboard_set_text(gtk_clipboard_get(GDK_SELECTION_CLIPBOARD),text_member(request,"text"),-1); clipboard_revision++; clipboard_sync_reply(pending,FALSE);
        } else { reply_error(reply,"剪贴板内容格式或大小无效。"); pending_free(pending); }
    } else if (!strcmp(name, "clipboardReadContent")) {
        gtk_clipboard_request_uris(gtk_clipboard_get(GDK_SELECTION_CLIPBOARD), clipboard_file_uris, pending_reply(reply, context));
    } else if (!strcmp(name, "chooseUploadFiles")) {
        if (app.picker_open) reply_error(reply, "请先完成当前文件窗口。");
        else {
            app.picker_open = TRUE;
            GtkWidget *dialog = gtk_file_chooser_dialog_new(ui_text("选择本机文件"), GTK_WINDOW(app.window), GTK_FILE_CHOOSER_ACTION_OPEN,
                ui_text("取消"), GTK_RESPONSE_CANCEL, ui_text("选择"), GTK_RESPONSE_ACCEPT, NULL);
            JsonNode *multiple = json_object_get_member(request, "multiple");
            gtk_file_chooser_set_select_multiple(GTK_FILE_CHOOSER(dialog), multiple && JSON_NODE_HOLDS_VALUE(multiple) && json_node_get_value_type(multiple) == G_TYPE_BOOLEAN && json_node_get_boolean(multiple));
            gtk_file_chooser_set_local_only(GTK_FILE_CHOOSER(dialog), TRUE);
            if (gtk_dialog_run(GTK_DIALOG(dialog)) == GTK_RESPONSE_ACCEPT) {
                GSList *list = gtk_file_chooser_get_filenames(GTK_FILE_CHOOSER(dialog));
                GPtrArray *paths = g_ptr_array_new_with_free_func(g_free);
                for (GSList *item = list; item; item = item->next) g_ptr_array_add(paths, item->data);
                g_slist_free(list);
                GError *error = NULL; char *json = remember_files(paths, &error);
                if (error) reply_error(reply, error->message); else reply_json(reply, context, json);
                g_free(json); g_clear_error(&error); g_ptr_array_unref(paths);
            } else reply_json(reply, context, "{\"canceled\":true}");
            gtk_widget_destroy(dialog); app.picker_open = FALSE;
        }
    } else if (!strcmp(name, "uploadHostFiles")) {
        const char *target = text_member(request, "target"), *id = text_member(request, "id"), *token = text_member(request, "selection");
        FileSelection *selected = file_selections ? g_hash_table_lookup(file_selections, token) : NULL;
        if (!(strcmp(target, "terminal") == 0 ? g_regex_match_simple("^[a-f0-9]{16}$", id, 0, 0) : g_uuid_string_is_valid(id)) || (strcmp(target, "browser") && strcmp(target, "desktop") && strcmp(target, "terminal"))) reply_error(reply, "文件上传目标无效。");
        else if (!selected || selected->expires <= g_get_monotonic_time()) {
            if (file_selections) g_hash_table_remove(file_selections, token);
            reply_error(reply, "文件选择已过期，请重新复制或选择文件。");
        } else {
            GPtrArray *paths = g_ptr_array_ref(selected->paths);
            g_hash_table_remove(file_selections, token);
            const char *connection = text_member(request, "connection_id");
            const char **args = g_new0(const char *, paths->len + 8);
            args[0] = app.backend; args[1] = "upload-files"; args[2] = "--json"; args[3] = "--connection"; args[4] = *connection ? connection : "local"; args[5] = target; args[6] = id;
            for (guint i = 0; i < paths->len; i++) args[i + 7] = g_ptr_array_index(paths, i);
            backend_arguments(args, 1800, browser_file_finished, pending_reply(reply, context));
            g_free(args); g_ptr_array_unref(paths);
        }
    } else if (!strcmp(name, "clipboardWrite")) {
        const char *text = text_member(request, "text");
        GtkClipboard *clipboard = gtk_clipboard_get(GDK_SELECTION_CLIPBOARD);
        gtk_clipboard_set_text(clipboard, text, -1);
        gtk_clipboard_store(clipboard);
        reply_json(reply, context, "true");
    } else if (!strcmp(name, "appearance")) {
        const char *mode = text_member(request, "mode");
        const char *background = text_member(request, "background");
        GdkRGBA color;
        gboolean valid = (!strcmp(mode, "light") || !strcmp(mode, "dark") || !strcmp(mode, "system")) &&
            strlen(background) == 7 && background[0] == '#' && strspn(background + 1, "0123456789abcdefABCDEF") == 6 &&
            gdk_rgba_parse(&color, background);
        if (!valid) reply_error(reply, "外观参数无效。");
        else {
            g_object_set(gtk_settings_get_default(), "gtk-application-prefer-dark-theme",
                !strcmp(mode, "system") ? app.initial_dark : !strcmp(mode, "dark"), NULL);
            app.background_color = color;
            app.has_background_color = TRUE;
            if (app.web) webkit_web_view_set_background_color(app.web, &color);
            reply_json(reply, context, "true");
        }
    } else if (!strcmp(name, "importTheme")) {
        if (app.picker_open) reply_error(reply, "请先完成当前文件选择。");
        else {
            char *path = choose_file(GTK_FILE_CHOOSER_ACTION_OPEN, "导入主题", NULL);
            if (!path) reply_string(reply, context, "");
            else {
                /* Only read a regular local file, with a hard allocation bound. */
                GFile *file = g_file_new_for_path(path);
                GFileInfo *info = g_file_query_info(file, G_FILE_ATTRIBUTE_STANDARD_TYPE "," G_FILE_ATTRIBUTE_STANDARD_SIZE,
                    G_FILE_QUERY_INFO_NONE, NULL, NULL);
                gboolean regular = info && g_file_info_get_file_type(info) == G_FILE_TYPE_REGULAR && g_file_info_get_size(info) <= MAX_THEME;
                GFileInputStream *stream = regular ? g_file_read(file, NULL, NULL) : NULL;
                char *contents = g_malloc(MAX_THEME + 2);
                gsize length = 0;
                gboolean ok = stream && g_input_stream_read_all(G_INPUT_STREAM(stream), contents, MAX_THEME + 1, &length, NULL, NULL);
                if (ok && length <= MAX_THEME && g_utf8_validate(contents, length, NULL) && !memchr(contents, 0, length)) {
                    contents[length] = 0;
                    reply_string(reply, context, contents);
                } else reply_error(reply, "请选择不超过 256 KB 的 UTF-8 JSON 主题文件。");
                g_clear_object(&stream); g_clear_object(&info); g_object_unref(file); g_free(contents);
            }
            g_free(path);
        }
    } else if (!strcmp(name, "saveRecoveryKey")) {
        const char *key = text_member(request, "key");
        if (strlen(key) != 71 || !g_regex_match_simple("^[A-F0-9]{8}(-[A-F0-9]{8}){7}$", key, 0, 0)) reply_error(reply, "恢复密钥格式无效。");
        else if (app.picker_open) reply_error(reply, "请先完成当前文件选择。");
        else {
            char *path = choose_file(GTK_FILE_CHOOSER_ACTION_SAVE, "保存账户恢复密钥", "账户恢复密钥.txt");
            char *contents = g_strconcat(key, "\n", NULL);
            if (!path) reply_json(reply, context, "false");
            else if (g_file_set_contents_full(path, contents, -1, G_FILE_SET_CONTENTS_CONSISTENT | G_FILE_SET_CONTENTS_DURABLE, 0600, NULL)) reply_json(reply, context, "true");
            else reply_error(reply, "恢复密钥未能保存，请检查保存位置的权限。");
            g_free(contents); g_free(path);
        }
    } else if (!strcmp(name, "exportTheme")) {
        const char *contents = text_member(request, "text");
        JsonParser *theme_parser = json_parser_new();
        gboolean valid = strlen(contents) <= MAX_THEME && json_parser_load_from_data(theme_parser, contents, -1, NULL) &&
            JSON_NODE_HOLDS_OBJECT(json_parser_get_root(theme_parser));
        JsonObject *theme = valid ? json_node_get_object(json_parser_get_root(theme_parser)) : NULL;
        JsonNode *version = theme ? json_object_get_member(theme, "version") : NULL;
        valid = theme && !strcmp(text_member(theme, "format"), "cage-theme") && version &&
            JSON_NODE_HOLDS_VALUE(version) && json_node_get_value_type(version) == G_TYPE_INT64 && json_node_get_int(version) == 1;
        if (!valid) reply_error(reply, "主题文件格式无效。");
        else if (app.picker_open) reply_error(reply, "请先完成当前文件选择。");
        else {
            char *path = choose_file(GTK_FILE_CHOOSER_ACTION_SAVE, "导出主题", text_member(request, "filename"));
            if (!path) reply_json(reply, context, "false");
            else if (g_file_set_contents(path, contents, -1, NULL)) reply_json(reply, context, "true");
            else reply_error(reply, "主题未能导出，请检查保存位置的权限。");
            g_free(path);
        }
        g_object_unref(theme_parser);
    } else if (!strcmp(name, "notificationStatus") || !strcmp(name, "requestNotificationPermission")) {
        notification_status(reply, context);
    } else if (!strcmp(name, "openNotificationSettings")) {
        reply_json(reply, context, "false");
    } else if (!strcmp(name, "showNotification")) {
        const char *title = text_member(request, "title");
        const char *body = text_member(request, "body");
        const char *terminal_id = text_member(request, "terminal_id");
        const char *connection_id = text_member(request, "connection_id");
        JsonNode *sound = request ? json_object_get_member(request, "sound") : NULL;
        gboolean valid_terminal = !*terminal_id || (strlen(terminal_id) == 16 && strspn(terminal_id, "0123456789abcdef") == 16);
        if (!*title || strlen(title) > 600 || !g_utf8_validate(title, -1, NULL)) reply_error(reply, "通知标题无效。");
        else if (strlen(body) > 1500 || !g_utf8_validate(body, -1, NULL)) reply_error(reply, "通知内容过长。");
        else if (!valid_terminal) reply_error(reply, "通知目标终端无效。");
        else if (strlen(connection_id) > 128) reply_error(reply, "通知连接标识无效。");
        else notification_send(reply, context, title, body, terminal_id, connection_id,
            !sound || !JSON_NODE_HOLDS_VALUE(sound) || json_node_get_value_type(sound) != G_TYPE_BOOLEAN || json_node_get_boolean(sound));
    } else if (!strcmp(name, "refreshLanguage")) {
        const char *args[] = {"interface-language", "--json", NULL};
        backend_arguments_limited(args, 4, 64 * 1024, language_refreshed, pending_reply(reply, context));
    } else if (!strcmp(name, "refreshSession")) {
        backend("desktop", app.needs_update ? "--existing" : NULL, session_refreshed, pending_reply(reply, context));
    } else if (!strcmp(name, "reconnect")) {
        reply_json(reply, context, "true"); bootstrap(NULL);
    } else if (!strcmp(name, "quitNow")) {
        reply_json(reply, context, "true"); g_idle_add(quit_idle, NULL);
    } else reply_error(reply, "未知原生操作。");
done:
    g_free(json); g_object_unref(parser);
    return TRUE;
}
static void install_bridge(void) {
    /* WebKitGTK does not expose the sender frame on its reply signal. Only the
     * exact workbench's top document receives this nonce; the host validates it
     * again, and the workbench CSP prohibits frames. The raw transport alone
     * confers no native capabilities. */
    g_free(app.token);
    app.token = g_uuid_string_random();
    char *token = quote(app.token);
    char *code = g_strdup_printf(
        "(()=>{'use strict';window.cageLinuxBridgeInit='injected';if(window!==window.top||location.protocol!=='http:'||location.hostname!=='localhost'||location.port!=='%d')return;"
        "window.cageLinuxBridgeInit='origin-valid';window.cageNativeIMEAvailable=true;try{"
        "const transport=window.webkit.messageHandlers.cageTransport;const token=%s;"
        "const cage=Object.freeze({postMessage(request){return transport.postMessage({token,request});}});"
        "Object.defineProperty(window,'cageNativeBridge',{value:cage,writable:false,configurable:false});"
        "window.cageLinuxBridgeInit='ready';}catch(error){window.cageLinuxBridgeInit=error.name;}"
        "})();", app.port, token);
    /* The host allowlist narrows injection; the script and native receiver
     * both enforce the exact controller port. */
    char *origin = g_strdup("http://localhost/*");
    const char *allowed[] = {origin, NULL};
    WebKitUserScript *user_script = webkit_user_script_new(code, WEBKIT_USER_CONTENT_INJECT_TOP_FRAME,
        WEBKIT_USER_SCRIPT_INJECT_AT_DOCUMENT_START, allowed, NULL);
    webkit_user_content_manager_remove_all_scripts(app.content);
    webkit_user_content_manager_add_script(app.content, user_script);
    webkit_user_script_unref(user_script);
    g_free(code); g_free(origin); g_free(token);
}
static void bootstrapped(JsonObject *object, const GError *error, gpointer data) {
    (void)data;
    app.bootstrapping = FALSE;
    gtk_widget_set_sensitive(app.retry, TRUE);
    if (error) {
        diagnostic_event(error->message);
        gboolean chinese = FALSE;
        for (const char *p = error->message; *p; p = g_utf8_next_char(p)) {
            gunichar value = g_utf8_get_char(p);
            if (value >= 0x4E00 && value <= 0x9FFF) { chinese = TRUE; break; }
        }
        if (chinese) show_error(error->message);
        else {
            char *code = g_strdup_printf("%d", error->code);
            char *message = ui_format("启动工作台失败：本机后台未返回可识别的原因（错误代码 {0}）。请点击“重试连接”；仍失败时请保留此提示并反馈给维护者。", code);
            show_error(message); g_free(message); g_free(code);
        }
        return;
    }
    const char *status = text_member(object, "status");
    app.needs_update = strcmp(status, "ready") != 0;
    if (!strcmp(status, "upgrade_pending")) {
        gtk_label_set_text(GTK_LABEL(app.status), text_member(object, "message"));
        gtk_widget_show(app.existing); gtk_widget_show(app.complete);
        return;
    }
    const char *url = text_member(object, "url");
    if ((strcmp(status, "ready") && strcmp(status, "existing")) || !valid_workbench(url, 0, TRUE)) {
        show_error("后台返回了无效的工作台入口。"); return;
    }
    GUri *uri = g_uri_parse(url, G_URI_FLAGS_NONE, NULL);
    app.port = g_uri_get_port(uri);
    g_uri_unref(uri);
    app.ready = FALSE;
    install_bridge();
    gtk_window_set_title(GTK_WINDOW(app.window), app.needs_update ? ui_text("Cage（更新待完成）") : "Cage");
    webkit_web_view_load_uri(app.web, url);
}
static void bootstrap(const char *option) {
    if (app.bootstrapping) return;
    app.bootstrapping = TRUE;
    gtk_label_set_text(GTK_LABEL(app.status), ui_text("正在连接 Cage 工作台…"));
    gtk_stack_set_visible_child_name(GTK_STACK(app.stack), "status");
    gtk_widget_hide(app.retry); gtk_widget_hide(app.existing); gtk_widget_hide(app.complete);
    backend("desktop", option, bootstrapped, NULL);
}
static void retry_clicked(GtkButton *button, gpointer data) { (void)button; (void)data; bootstrap(NULL); }
static void existing_clicked(GtkButton *button, gpointer data) { (void)button; (void)data; bootstrap("--existing"); }
static void complete_clicked(GtkButton *button, gpointer data) {
    (void)button; (void)data;
    GtkWidget *dialog = gtk_message_dialog_new(GTK_WINDOW(app.window), GTK_DIALOG_MODAL, GTK_MESSAGE_QUESTION, GTK_BUTTONS_OK_CANCEL,
        "%s", ui_text("请先保存文件。继续后将停止 Cage 环境并完成后台更新，终端任务会结束，项目和登录状态会保留。"));
    gboolean accepted = gtk_dialog_run(GTK_DIALOG(dialog)) == GTK_RESPONSE_OK;
    gtk_widget_destroy(dialog);
    if (accepted) bootstrap("--complete-update");
}
static gboolean decide_policy(WebKitWebView *web, WebKitPolicyDecision *decision, WebKitPolicyDecisionType type, gpointer data) {
    (void)web; (void)data;
    if (type == WEBKIT_POLICY_DECISION_TYPE_NEW_WINDOW_ACTION) { webkit_policy_decision_ignore(decision); return TRUE; }
    if (type == WEBKIT_POLICY_DECISION_TYPE_NAVIGATION_ACTION) {
        WebKitNavigationAction *navigation = webkit_navigation_policy_decision_get_navigation_action(WEBKIT_NAVIGATION_POLICY_DECISION(decision));
        const char *uri = webkit_uri_request_get_uri(webkit_navigation_action_get_request(navigation));
        if (!valid_workbench(uri, app.port, FALSE)) { webkit_policy_decision_ignore(decision); return TRUE; }
    }
    return FALSE;
}
static gboolean handle_permission(WebKitWebView *web, WebKitPermissionRequest *request, gpointer data) {
    (void)data;
    if (WEBKIT_IS_USER_MEDIA_PERMISSION_REQUEST(request)) {
        WebKitUserMediaPermissionRequest *media = WEBKIT_USER_MEDIA_PERMISSION_REQUEST(request);
        const char *uri = webkit_web_view_get_uri(web);
        if (webkit_user_media_permission_is_for_audio_device(media) &&
            !webkit_user_media_permission_is_for_video_device(media) &&
            valid_workbench(uri, app.port, FALSE)) {
            webkit_permission_request_allow(request);
            return TRUE;
        }
    }
    webkit_permission_request_deny(request);
    return TRUE;
}
static gboolean workbench_file_chooser(WebKitWebView *web, WebKitFileChooserRequest *request, gpointer data) {
    (void)data;
    /* Browser uploads, avatars and imports use the host's native HTML chooser. */
    if (valid_workbench(webkit_web_view_get_uri(web), app.port, FALSE)) return FALSE;
    webkit_file_chooser_request_cancel(request);
    return TRUE;
}
static void deny_download(WebKitWebContext *context, WebKitDownload *download, gpointer data) {
    (void)context; (void)data; webkit_download_cancel(download);
}
static gboolean deny_context_menu(WebKitWebView *web, WebKitContextMenu *menu, GdkEvent *event, WebKitHitTestResult *hit, gpointer data) {
    (void)web; (void)menu; (void)event; (void)hit; (void)data; return TRUE;
}
static void load_changed(WebKitWebView *web, WebKitLoadEvent event, gpointer data) {
    (void)data;
    if (web != app.web) return;
    if (event == WEBKIT_LOAD_STARTED) app.navigation_failed = FALSE;
    if (event == WEBKIT_LOAD_FINISHED && !app.navigation_failed && valid_workbench(webkit_web_view_get_uri(web), app.port, FALSE)) {
        gtk_stack_set_visible_child_name(GTK_STACK(app.stack), "web");
        gtk_widget_grab_focus(GTK_WIDGET(web));
        publish_visibility();
    }
}
static gboolean load_failed(WebKitWebView *web, WebKitLoadEvent event, const char *uri, GError *error, gpointer data) {
    (void)event; (void)uri; (void)data;
    if (web != app.web) return TRUE;
    app.navigation_failed = TRUE;
    if (error) diagnostic_event(error->message);
    char *code = g_strdup_printf("%d", error ? error->code : 0);
    char *message = ui_format("打开工作台失败：内置网页组件未能加载本机工作台（错误代码 {0}）。请点击“重试连接”；仍失败时请保留此错误提示并反馈给维护者。后台任务不会因此自动结束。", code);
    show_error(message);
    g_free(message); g_free(code);
    return TRUE;
}
static gboolean retry_software_fallback(gpointer data) {
    (void)data;
    bootstrap(NULL);
    return G_SOURCE_REMOVE;
}
static void process_terminated(WebKitWebView *web, WebKitWebProcessTerminationReason reason, gpointer data) {
    (void)data;
    if (web != app.web) return;
    const char *kind = reason == WEBKIT_WEB_PROCESS_CRASHED ? "crashed"
        : reason == WEBKIT_WEB_PROCESS_EXCEEDED_MEMORY_LIMIT ? "exceeded-memory"
        : "terminated-by-api";
    char *detail = g_strdup_printf(
        "WebKitWebProcess terminated (reason=%s, webkit=%u.%u.%u, media_isolated=%d, software_fallback=%d)",
        kind, webkit_get_major_version(), webkit_get_minor_version(), webkit_get_micro_version(),
        app.media_plugins_isolated ? 1 : 0, app.software_fallback ? 1 : 0);
    diagnostic_event(detail);
    g_free(detail);
    if (reason == WEBKIT_WEB_PROCESS_CRASHED && !app.software_fallback) {
        app.software_fallback = TRUE;
        WebKitSettings *web_settings = webkit_web_view_get_settings(web);
        webkit_settings_set_hardware_acceleration_policy(web_settings, WEBKIT_HARDWARE_ACCELERATION_POLICY_NEVER);
        webkit_settings_set_enable_media(web_settings, FALSE);
        if (!app.ready) {
            g_idle_add(retry_software_fallback, NULL);
            return;
        }
    }
    show_error("工作台显示进程已退出。后台任务保留，可重试连接。");
}
static void install_web_view(void) {
    if (app.web) {
        WebKitWebView *previous = app.web;
        app.web = NULL;
        webkit_web_view_stop_loading(previous);
        gtk_container_remove(GTK_CONTAINER(app.stack), GTK_WIDGET(previous));
    }
    app.web = WEBKIT_WEB_VIEW(g_object_new(WEBKIT_TYPE_WEB_VIEW, "web-context", app.web_context, "user-content-manager", app.content, NULL));
    input_initialize(app.web);
    WebKitSettings *web_settings = webkit_web_view_get_settings(app.web);
    webkit_settings_set_enable_developer_extras(web_settings, FALSE);
    webkit_settings_set_javascript_can_open_windows_automatically(web_settings, FALSE);
    webkit_settings_set_enable_write_console_messages_to_stdout(web_settings, FALSE);
    webkit_settings_set_enable_webgl(web_settings, FALSE);
    webkit_settings_set_enable_media_stream(web_settings, !app.software_fallback);
    if (app.software_fallback) {
        webkit_settings_set_hardware_acceleration_policy(web_settings, WEBKIT_HARDWARE_ACCELERATION_POLICY_NEVER);
        webkit_settings_set_enable_media(web_settings, FALSE);
    }
#if WEBKIT_CHECK_VERSION(2, 38, 0)
    webkit_settings_set_enable_webrtc(web_settings, FALSE);
#endif
    if (app.has_background_color)
        webkit_web_view_set_background_color(app.web, &app.background_color);
    g_signal_connect(app.web, "decide-policy", G_CALLBACK(decide_policy), NULL);
    g_signal_connect(app.web, "permission-request", G_CALLBACK(handle_permission), NULL);
    g_signal_connect(app.web, "run-file-chooser", G_CALLBACK(workbench_file_chooser), NULL);
    g_signal_connect(app.web, "context-menu", G_CALLBACK(deny_context_menu), NULL);
    g_signal_connect(app.web, "load-changed", G_CALLBACK(load_changed), NULL);
    g_signal_connect(app.web, "load-failed", G_CALLBACK(load_failed), NULL);
    g_signal_connect(app.web, "web-process-terminated", G_CALLBACK(process_terminated), NULL);
    gtk_stack_add_named(GTK_STACK(app.stack), GTK_WIDGET(app.web), "web");
    gtk_widget_show(GTK_WIDGET(app.web));
}
typedef struct { WebKitWebView *web; GCancellable *cancel; guint deadline; gboolean timed_out; } QuitProbe;
static gboolean quit_probe_timeout(gpointer data) {
    QuitProbe *probe = data;
    gboolean current = (probe->web == app.web);
    probe->deadline = 0;
    probe->timed_out = TRUE;
    app.quit_pending = FALSE;
    g_cancellable_cancel(probe->cancel);
    if (current) {
        diagnostic_event("Workbench did not respond to quit request within 1.5s; exiting desktop window");
        g_application_quit(G_APPLICATION(app.application));
    }
    return G_SOURCE_REMOVE;
}
static void quit_probe_finished(GObject *source, GAsyncResult *result, gpointer data) {
    QuitProbe *probe = data;
    GError *error = NULL;
    JSCValue *value = webkit_web_view_evaluate_javascript_finish(WEBKIT_WEB_VIEW(source), result, &error);
    if (probe->deadline) { g_source_remove(probe->deadline); probe->deadline = 0; }
    if (!probe->timed_out) {
        app.quit_pending = FALSE;
        if (WEBKIT_WEB_VIEW(source) == app.web) {
            gboolean handled = value && !error && jsc_value_is_boolean(value) && jsc_value_to_boolean(value);
            if (!handled) {
                diagnostic_event("Workbench quit handoff failed; exiting desktop window");
                g_application_quit(G_APPLICATION(app.application));
            }
        }
    }
    if (value) g_object_unref(value);
    g_clear_error(&error);
    g_object_unref(probe->cancel);
    g_free(probe);
}
static void request_quit(void) {
    if (!app.web || gtk_stack_get_visible_child(GTK_STACK(app.stack)) != GTK_WIDGET(app.web) ||
        !app.port || !valid_workbench(webkit_web_view_get_uri(app.web), app.port, FALSE)) {
        g_application_quit(G_APPLICATION(app.application));
        return;
    }
    if (app.quit_pending) return;
    app.quit_pending = TRUE;
    QuitProbe *probe = g_new0(QuitProbe, 1);
    probe->web = app.web;
    probe->cancel = g_cancellable_new();
    probe->deadline = g_timeout_add(1500, quit_probe_timeout, probe);
    webkit_web_view_evaluate_javascript(app.web,
        "typeof window.cageNativeAction==='function'?(window.cageNativeAction('quit'),true):false",
        -1, NULL, NULL, probe->cancel, quit_probe_finished, probe);
}
static void menu_action(GSimpleAction *simple, GVariant *parameter, gpointer data) {
    (void)parameter; (void)data;
    const char *name = g_action_get_name(G_ACTION(simple));
    if (!strcmp(name, "closeWindow")) { gtk_widget_hide(app.window); publish_visibility(); }
    else if (!strcmp(name, "reloadWorkbench")) {
        gtk_widget_show(app.window);
        gtk_window_deiconify(GTK_WINDOW(app.window));
        gtk_window_present(GTK_WINDOW(app.window));
        if (!app.bootstrapping) {
            diagnostic_event("Reloading workbench WebView");
            install_web_view();
            bootstrap(app.needs_update ? "--existing" : NULL);
        }
    } else if (!strcmp(name, "quit")) {
        request_quit();
    } else if (!strcmp(name, "updates")) {
        GtkWidget *dialog = gtk_message_dialog_new(GTK_WINDOW(app.window), GTK_DIALOG_MODAL, GTK_MESSAGE_INFO, GTK_BUTTONS_CLOSE,
            "%s", ui_text("Linux 版本通过安装包更新。请保存文件、停止环境并退出 Cage，再安装已核对摘要的官方新版本。"));
        gtk_dialog_run(GTK_DIALOG(dialog)); gtk_widget_destroy(dialog);
    } else if (!strcmp(name, "about")) {
        gtk_show_about_dialog(GTK_WINDOW(app.window), "program-name", "Cage", "version", CAGE_VERSION,
            "comments", ui_text("本机隔离编码工作区"), "license-type", GTK_LICENSE_MIT_X11, NULL);
    } else action(name);
}
static void update_menu(void) {
    GMenu *file = g_menu_new(), *settings = g_menu_new(), *help = g_menu_new();
    g_menu_append(file, ui_text("打开项目…"), "app.openProject");
    g_menu_append(file, ui_text("新建网页"), "app.newTab");
    g_menu_append(file, ui_text("新建终端"), "app.newTerminal");
    g_menu_append(file, ui_text("关闭当前标签"), "app.closeTab");
    g_menu_append(file, ui_text("重新加载界面"), "app.reloadWorkbench");
    g_menu_append(file, ui_text("关闭窗口（保留环境）"), "app.closeWindow");
    g_menu_append(file, ui_text("退出 Cage…"), "app.quit");
    g_menu_append(settings, ui_text("打开设置"), "app.settings");
    g_menu_append(settings, ui_text("应用更新…"), "app.updates");
    g_menu_append(help, ui_text("关于 Cage"), "app.about");
    g_menu_remove_all(app.menu);
    g_menu_append_submenu(app.menu, ui_text("文件"), G_MENU_MODEL(file));
    g_menu_append_submenu(app.menu, ui_text("设置"), G_MENU_MODEL(settings));
    g_menu_append_submenu(app.menu, ui_text("帮助"), G_MENU_MODEL(help));
    g_object_unref(file); g_object_unref(settings); g_object_unref(help);
}
static void create_window(void) {
    app.window = gtk_application_window_new(app.application);
    gtk_window_set_title(GTK_WINDOW(app.window), "Cage");
    gtk_window_set_default_size(GTK_WINDOW(app.window), 1280, 850);
    gtk_widget_set_size_request(app.window, 820, 580);
    gtk_window_set_icon_name(GTK_WINDOW(app.window), "io.github.lee_xeo.Cage");
    g_signal_connect(app.window, "delete-event", G_CALLBACK(close_window), NULL);
    g_signal_connect(app.window, "window-state-event", G_CALLBACK(visibility_changed), NULL);
    g_signal_connect(app.window, "map-event", G_CALLBACK(visibility_changed), NULL);
    g_signal_connect(app.window, "unmap-event", G_CALLBACK(visibility_changed), NULL);
    g_signal_connect(app.window, "notify::is-active", G_CALLBACK(focus_changed), NULL);
    g_object_get(gtk_settings_get_default(), "gtk-application-prefer-dark-theme", &app.initial_dark, NULL);
    GtkWidget *layout = gtk_box_new(GTK_ORIENTATION_VERTICAL, 0);
    gtk_container_add(GTK_CONTAINER(app.window), layout);
    app.menu = g_menu_new();
    /* Workbench shortcuts stay in JavaScript so terminal/browser focus rules apply. */
    const char *actions[] = {"openProject", "newTab", "newTerminal", "closeTab", "reloadWorkbench", "closeWindow", "quit", "settings", "updates", "about"};
    for (guint i = 0; i < G_N_ELEMENTS(actions); i++) {
        GSimpleAction *simple = g_simple_action_new(actions[i], NULL);
        g_signal_connect(simple, "activate", G_CALLBACK(menu_action), NULL);
        g_action_map_add_action(G_ACTION_MAP(app.application), G_ACTION(simple));
        g_object_unref(simple);
    }
    const char *quit_accels[] = {"<Primary>q", NULL};
    gtk_application_set_accels_for_action(app.application, "app.quit", quit_accels);
    update_menu();
    gtk_box_pack_start(GTK_BOX(layout), gtk_menu_bar_new_from_model(G_MENU_MODEL(app.menu)), FALSE, FALSE, 0);
    app.stack = gtk_stack_new();
    gtk_box_pack_start(GTK_BOX(layout), app.stack, TRUE, TRUE, 0);
    GtkWidget *status_box = gtk_box_new(GTK_ORIENTATION_VERTICAL, 16);
    gtk_widget_set_margin_start(status_box, 48); gtk_widget_set_margin_end(status_box, 48);
    gtk_widget_set_margin_top(status_box, 48); gtk_widget_set_valign(status_box, GTK_ALIGN_START);
    app.status = gtk_label_new(ui_text("正在启动 Cage 工作台…"));
    gtk_label_set_line_wrap(GTK_LABEL(app.status), TRUE);
    gtk_label_set_xalign(GTK_LABEL(app.status), 0);
    gtk_box_pack_start(GTK_BOX(status_box), app.status, FALSE, FALSE, 0);
    app.retry = gtk_button_new_with_label(ui_text("重试连接"));
    app.existing = gtk_button_new_with_label(ui_text("打开现有环境保存文件"));
    app.complete = gtk_button_new_with_label(ui_text("保存后停止环境并完成更新"));
    GtkWidget *buttons[] = {app.retry, app.existing, app.complete};
    for (guint i = 0; i < G_N_ELEMENTS(buttons); i++) {
        gtk_widget_set_halign(buttons[i], GTK_ALIGN_START);
        gtk_box_pack_start(GTK_BOX(status_box), buttons[i], FALSE, FALSE, 0);
    }
    g_signal_connect(app.retry, "clicked", G_CALLBACK(retry_clicked), NULL);
    g_signal_connect(app.existing, "clicked", G_CALLBACK(existing_clicked), NULL);
    g_signal_connect(app.complete, "clicked", G_CALLBACK(complete_clicked), NULL);
    gtk_stack_add_named(GTK_STACK(app.stack), status_box, "status");
    /* Workbench layout persists within this private Cage profile. No system
     * browser profile or guest browser data is shared with this WebView. */
    WebKitWebsiteDataManager *storage = webkit_website_data_manager_new(
        "base-data-directory", app.web_data, "base-cache-directory", app.web_cache, NULL);
    app.web_context = webkit_web_context_new_with_website_data_manager(storage);
    g_object_unref(storage);
    webkit_web_context_set_sandbox_enabled(app.web_context, TRUE);
    if (app.media_plugins_isolated) {
        char *gst_root = g_build_filename(app.web_cache, "gstreamer", NULL);
        char *gst_plugins = g_build_filename(gst_root, "plugins", NULL);
        webkit_web_context_add_path_to_sandbox(app.web_context, gst_root, FALSE);
        webkit_web_context_add_path_to_sandbox(app.web_context, gst_plugins, TRUE);
        g_free(gst_plugins);
        g_free(gst_root);
    }
    webkit_website_data_manager_set_network_proxy_settings(webkit_web_context_get_website_data_manager(app.web_context), WEBKIT_NETWORK_PROXY_MODE_NO_PROXY, NULL);
    g_signal_connect(app.web_context, "download-started", G_CALLBACK(deny_download), NULL);
    app.content = webkit_user_content_manager_new();
    g_signal_connect(app.content, "script-message-with-reply-received::cageTransport", G_CALLBACK(bridge_received), NULL);
    webkit_user_content_manager_register_script_message_handler_with_reply(app.content, "cageTransport", NULL);
    install_web_view();
    gtk_widget_show_all(app.window);
    bootstrap(NULL);
}
static void activate(GtkApplication *application, gpointer data) {
    (void)application; (void)data;
    if (!app.window) create_window();
    else { gtk_widget_show(app.window); gtk_window_deiconify(GTK_WINDOW(app.window)); gtk_window_present(GTK_WINDOW(app.window)); publish_visibility(); }
}
static gboolean smoke_deadline(gpointer data) {
    (void)data;
    g_printerr("Linux smoke: timed out before a ready, authenticated workbench.\n");
    g_application_quit(G_APPLICATION(app.application));
    return G_SOURCE_REMOVE;
}
static void smoke_diagnostics_ready(GObject *source, GAsyncResult *result, gpointer data) {
    (void)data;
    GError *error = NULL;
    JSCValue *value = webkit_web_view_call_async_javascript_function_finish(WEBKIT_WEB_VIEW(source), result, &error);
    if (value) {
        char *json = jsc_value_to_json(value, 0);
        g_printerr("Linux smoke startup diagnostics: %s\n", json ? json : "unavailable");
        g_free(json); g_object_unref(value);
    } else g_printerr("Linux smoke: JavaScript diagnostics unavailable.\n");
    g_clear_error(&error);
}
static gboolean smoke_diagnostics(gpointer data) {
    (void)data;
    if (app.ready || !app.web) return G_SOURCE_REMOVE;
    const char *body =
        "const account=await fetch('/api/account');const state=await fetch('/api/status');const settings=await fetch('/api/settings');"
        "let unlocked=false;try{unlocked=(await account.json()).unlocked===true;}catch{}"
        "const descriptor=Object.getOwnPropertyDescriptor(window,'webkit');"
        "return {bridgeAvailable:!!window.cageNativeBridge,bridgeInit:window.cageLinuxBridgeInit||'not-injected',webkitConfigurable:descriptor?.configurable,transportPresent:!!window.webkit?.messageHandlers?.cageTransport,readyState:document.readyState,"
        "renderedButtons:document.querySelectorAll('button').length,accountStatus:account.status,accountUnlocked:unlocked,statusStatus:state.status,settingsStatus:settings.status};";
    webkit_web_view_call_async_javascript_function(app.web, body, -1, NULL, NULL, "cage-smoke-diagnostics", NULL, smoke_diagnostics_ready, NULL);
    return G_SOURCE_REMOVE;
}
static int self_test(void) {
    g_assert_true(valid_workbench("http://localhost:23456/#secret", 23456, TRUE));
    g_assert_true(valid_workbench("http://localhost:23456/", 23456, FALSE));
    g_assert_false(valid_workbench("http://localhost:23456/", 23456, TRUE));
    g_assert_false(valid_workbench("http://localhost:23457/#secret", 23456, TRUE));
    g_assert_false(valid_workbench("http://127.0.0.1:23456/#secret", 23456, TRUE));
    g_assert_false(valid_workbench("http://localhost.evil.test:23456/#secret", 23456, TRUE));
    g_assert_false(valid_workbench("http://user@localhost:23456/#secret", 23456, TRUE));
    g_assert_false(valid_workbench("https://localhost:23456/#secret", 23456, TRUE));
    g_assert_false(valid_workbench("file:///tmp/workbench.html", 0, FALSE));
    g_assert_false(valid_workbench("javascript:alert(1)", 0, FALSE));
    g_assert_false(valid_workbench("http://localhost:80/#secret", 0, TRUE));
    g_print("Linux desktop origin policy: 11 checks passed.\n");
    return 0;
}
int main(int argc, char **argv) {
    if (argc == 2 && !strcmp(argv[1], "--self-test")) return self_test();
    if (argc == 2 && !strcmp(argv[1], "--version")) { g_print("Cage %s\n", CAGE_VERSION); return 0; }
    if (argc == 3 && !strcmp(argv[1], "--smoke-test") && g_path_is_absolute(argv[2]) && g_getenv("CLAUDE_CAGE_CONFIG")) {
        app.smoke_output = g_strdup(argv[2]); app.result = 1;
        app.storage_phase = g_getenv("CAGE_SMOKE_STORAGE_PHASE");
        app.storage_marker = g_getenv("CAGE_SMOKE_STORAGE_MARKER");
        if (app.storage_phase && (g_strcmp0(app.storage_phase, "seed") && g_strcmp0(app.storage_phase, "verify"))) return 2;
        if (app.storage_phase && (!app.storage_marker || !*app.storage_marker || strlen(app.storage_marker) > 128)) return 2;
    } else if (argc != 1) {
        g_printerr("Usage: cage-desktop [--version | --self-test | --smoke-test /absolute/report.json]\nSmoke tests require an isolated CLAUDE_CAGE_CONFIG with an unlocked test account.\n");
        return 2;
    }
    if (geteuid() == 0) { g_printerr("Cage 桌面需要以普通用户运行，请勿使用 sudo 启动。\n"); return 1; }
    char *executable = g_file_read_link("/proc/self/exe", NULL);
    if (!executable) { g_printerr("无法定位 Cage 安装目录。\n"); return 1; }
    char *directory = g_path_get_dirname(executable);
    app.backend = g_build_filename(directory, "cage", NULL);
    g_free(directory); g_free(executable);
    if (!g_file_test(app.backend, G_FILE_TEST_IS_EXECUTABLE)) { g_printerr("安装目录缺少 Cage 后台，请重新安装。\n"); return 1; }
    const char *configured = g_getenv("CLAUDE_CAGE_CONFIG");
    app.profile = configured && *configured ? g_canonicalize_filename(configured, NULL)
        : CAGE_CLIENT_ONLY ? g_build_filename(g_get_home_dir(), ".claude-cage", "client", "config.json", NULL)
        : g_build_filename(g_get_home_dir(), ".claude-cage", "config.json", NULL);
    g_setenv("CLAUDE_CAGE_CONFIG", app.profile, TRUE);
    load_language();
    g_log_set_default_handler(diagnostic_glib, NULL);
    if (!prepare_web_storage()) {
        g_printerr("无法创建 Cage 私有界面数据目录，请检查配置目录的所有者、权限或符号链接。\n");
        return 1;
    }
    app.media_plugins_isolated = media_plugins_isolate(app.web_cache, media_plugins_system_dirs);
    app.dmabuf_default_set = media_plugins_disable_dmabuf_default("");
    char *hash = g_compute_checksum_for_string(G_CHECKSUM_SHA256, app.profile, -1);
    char *id = g_strdup_printf("app.cage.desktop.p%.24s", hash);
    app.application = gtk_application_new(id, G_APPLICATION_FLAGS_NONE);
    g_free(hash); g_free(id);
    g_signal_connect(app.application, "activate", G_CALLBACK(activate), NULL);
    if (app.smoke_output) {
        g_timeout_add_seconds(120, smoke_deadline, NULL);
        g_timeout_add_seconds(15, smoke_diagnostics, NULL);
    }
    int status = g_application_run(G_APPLICATION(app.application), 1, argv);
    if (app.window) gtk_widget_destroy(app.window);
    g_object_unref(app.application);
    g_free(app.backend); g_free(app.profile); g_free(app.token); g_free(app.smoke_output);
    g_free(app.web_data); g_free(app.web_cache);
    g_clear_object(&app.web_context);
    g_clear_object(&app.content);
    g_clear_object(&app.menu);
    g_clear_pointer(&app.messages, json_object_unref);
    return status ? status : app.result;
}

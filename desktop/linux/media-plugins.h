/* GStreamer plugins and renderer defaults seen by WebKit's processes.
 *
 * WebKit loads the system GStreamer plugins inside its sandboxed web process.
 * Two plugins crash WebKitWebProcess there over and over, so the workbench
 * cannot start on such hosts:
 *   - libgstnvcodec.so (NVIDIA codecs) initializes CUDA and fails on a device
 *     node the sandbox does not provide;
 *   - libgstlibav.so (gst-libav) crashes the web process while the plugin is
 *     scanned and loaded.
 * When either plugin is installed, give WebKit a private plugin directory that
 * links every other system plugin, and a private registry so the system's own
 * registry cache is not rewritten. The cost is that WebKit loses NVIDIA and
 * FFmpeg (libav) decoding; the workbench needs neither, and the remaining
 * system decoders still load. Hosts without these plugins, and hosts whose
 * GStreamer paths are configured explicitly, are left unchanged. The sandbox
 * stays enabled.
 *
 * Plain POSIX only, so the logic can be tested without GTK or WebKit. */
#ifndef CAGE_LINUX_MEDIA_PLUGINS_H
#define CAGE_LINUX_MEDIA_PLUGINS_H

#include <dirent.h>
#include <errno.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

#ifndef PATH_MAX
#define PATH_MAX 4096
#endif

static const char *const media_plugins_excluded[] = {"libgstnvcodec.so", "libgstlibav.so", NULL};

static const char *const media_plugins_system_dirs[] = {
#if defined(__x86_64__)
    "/usr/lib/x86_64-linux-gnu/gstreamer-1.0",
#elif defined(__aarch64__)
    "/usr/lib/aarch64-linux-gnu/gstreamer-1.0",
#endif
    "/usr/lib64/gstreamer-1.0",
    "/usr/lib/gstreamer-1.0",
    NULL,
};

static int media_plugins_excluded_name(const char *name) {
    for (int i = 0; media_plugins_excluded[i]; i++)
        if (!strcmp(name, media_plugins_excluded[i])) return 1;
    return 0;
}

static int media_plugins_is_plugin(const char *name) {
    size_t length = strlen(name);
    return length > 9 && !strncmp(name, "libgst", 6) && !strcmp(name + length - 3, ".so");
}

static int media_plugins_join(char *out, size_t size, const char *dir, const char *name) {
    int written = snprintf(out, size, "%s/%s", dir, name);
    return written > 0 && (size_t)written < size;
}

static int media_plugins_exists(const char *dir, const char *name) {
    char path[PATH_MAX];
    struct stat info;
    return media_plugins_join(path, sizeof path, dir, name) && stat(path, &info) == 0 && S_ISREG(info.st_mode);
}

static int media_plugins_ensure_dir(const char *path) {
    if (mkdir(path, 0700) != 0 && errno != EEXIST) return 0;
    struct stat info;
    return lstat(path, &info) == 0 && S_ISDIR(info.st_mode) && info.st_uid == geteuid();
}

/* The system plugin directory, or NULL. */
static const char *media_plugins_system_dir(const char *const *candidates) {
    for (int i = 0; candidates[i]; i++)
        if (media_plugins_exists(candidates[i], "libgstcoreelements.so")) return candidates[i];
    return NULL;
}

/* Brings private_dir in line with system: one symbolic link per plugin to keep.
 * Only links are created or removed; any other entry aborts the update. */
static int media_plugins_sync(const char *system, const char *private_dir) {
    if (!media_plugins_ensure_dir(private_dir)) return 0;
    DIR *existing = opendir(private_dir);
    if (!existing) return 0;
    int ok = 1;
    struct dirent *entry;
    struct stat info;
    while (ok && (entry = readdir(existing))) {
        if (!strcmp(entry->d_name, ".") || !strcmp(entry->d_name, "..")) continue;
        char path[PATH_MAX], target[PATH_MAX], want[PATH_MAX];
        if (!media_plugins_join(path, sizeof path, private_dir, entry->d_name) || lstat(path, &info) != 0) { ok = 0; break; }
        if (!S_ISLNK(info.st_mode)) { ok = 0; break; }
        ssize_t length = readlink(path, target, sizeof target - 1);
        if (length < 0) { ok = 0; break; }
        target[length] = 0;
        int keep = media_plugins_is_plugin(entry->d_name) && !media_plugins_excluded_name(entry->d_name) &&
            media_plugins_exists(system, entry->d_name) && media_plugins_join(want, sizeof want, system, entry->d_name) &&
            !strcmp(target, want);
        if (!keep && unlink(path) != 0 && errno != ENOENT) ok = 0;
    }
    closedir(existing);
    if (!ok) return 0;
    DIR *plugins = opendir(system);
    if (!plugins) return 0;
    while (ok && (entry = readdir(plugins))) {
        if (!media_plugins_is_plugin(entry->d_name) || media_plugins_excluded_name(entry->d_name) ||
            !media_plugins_exists(system, entry->d_name)) continue;
        char source[PATH_MAX], link_path[PATH_MAX];
        if (!media_plugins_join(source, sizeof source, system, entry->d_name) ||
            !media_plugins_join(link_path, sizeof link_path, private_dir, entry->d_name)) { ok = 0; break; }
        if (symlink(source, link_path) != 0 && errno != EEXIST) ok = 0;
    }
    closedir(plugins);
    return ok;
}

/* Returns 1 when WebKit's processes will use the private plugin directory. */
static int media_plugins_isolate(const char *cache_dir, const char *const *candidates) {
    const char *configured[] = {"GST_PLUGIN_SYSTEM_PATH_1_0", "GST_PLUGIN_SYSTEM_PATH", "GST_REGISTRY_1_0", "GST_REGISTRY", NULL};
    for (int i = 0; configured[i]; i++)
        if (getenv(configured[i])) return 0;
    const char *system = media_plugins_system_dir(candidates);
    int needed = 0;
    for (int i = 0; system && media_plugins_excluded[i]; i++)
        needed |= media_plugins_exists(system, media_plugins_excluded[i]);
    if (!needed) return 0;
    char root[PATH_MAX], private_dir[PATH_MAX], registry[PATH_MAX];
    if (!media_plugins_join(root, sizeof root, cache_dir, "gstreamer") ||
        !media_plugins_join(private_dir, sizeof private_dir, root, "plugins") ||
        !media_plugins_join(registry, sizeof registry, root, "registry.bin")) return 0;
    if (!media_plugins_ensure_dir(root) || !media_plugins_sync(system, private_dir)) return 0;
    if (setenv("GST_PLUGIN_SYSTEM_PATH_1_0", private_dir, 1) != 0) return 0;
    if (setenv("GST_REGISTRY_1_0", registry, 1) != 0) {
        unsetenv("GST_PLUGIN_SYSTEM_PATH_1_0");
        return 0;
    }
    return 1;
}

/* Returns 1 when the NVIDIA proprietary kernel driver is loaded. root is
 * prepended to the /proc and /sys paths ("" on a real system, a fixture
 * directory in tests). */
static int media_plugins_nvidia_loaded(const char *root) {
    const char *markers[] = {"proc/driver/nvidia/version", "sys/module/nvidia", NULL};
    for (int i = 0; markers[i]; i++) {
        char path[PATH_MAX];
        if (!media_plugins_join(path, sizeof path, root && *root ? root : "/", markers[i])) continue;
        if (access(path, F_OK) == 0) return 1;
    }
    return 0;
}

/* WebKitGTK's DMA-BUF renderer crashes or blanks WebKitWebProcess with the
 * NVIDIA proprietary driver. Disable it only there: on other GPUs it is the
 * faster path, and turning it off costs CPU and frame rate in the remote
 * screen. An explicit WEBKIT_DISABLE_DMABUF_RENDERER from the caller (either
 * value) always wins. Returns 1 if this process set the variable. */
static int media_plugins_disable_dmabuf_default(const char *root) {
    if (getenv("WEBKIT_DISABLE_DMABUF_RENDERER")) return 0;
    if (!media_plugins_nvidia_loaded(root)) return 0;
    return setenv("WEBKIT_DISABLE_DMABUF_RENDERER", "1", 0) == 0;
}

#endif

/* POSIX unit tests for desktop/linux/media-plugins.h (no GTK or WebKit). */
#define _POSIX_C_SOURCE 200809L
#ifdef __APPLE__
#define _DARWIN_C_SOURCE
#endif
#include "media-plugins.h"

#include <assert.h>
#include <fcntl.h>

static void write_dummy(const char *dir, const char *name) {
    char path[PATH_MAX];
    assert(media_plugins_join(path, sizeof path, dir, name));
    int fd = open(path, O_CREAT | O_WRONLY | O_TRUNC, 0600);
    assert(fd >= 0);
    assert(close(fd) == 0);
}

static void make_dirs(const char *base, const char *relative) {
    char path[PATH_MAX];
    assert(media_plugins_join(path, sizeof path, base, relative));
    for (char *slash = path + strlen(base) + 1; (slash = strchr(slash, '/')); slash++) {
        *slash = 0;
        assert(mkdir(path, 0700) == 0 || errno == EEXIST);
        *slash = '/';
    }
    assert(mkdir(path, 0700) == 0 || errno == EEXIST);
}

static void clear_gst_env(void) {
    unsetenv("GST_PLUGIN_SYSTEM_PATH_1_0");
    unsetenv("GST_PLUGIN_SYSTEM_PATH");
    unsetenv("GST_REGISTRY_1_0");
    unsetenv("GST_REGISTRY");
}

static int path_exists_lstat(const char *path) {
    struct stat info;
    return lstat(path, &info) == 0;
}

static void remove_tree(const char *path) {
    struct stat info;
    if (lstat(path, &info) != 0) return;
    if (S_ISDIR(info.st_mode)) {
        DIR *dir = opendir(path);
        if (dir) {
            struct dirent *entry;
            while ((entry = readdir(dir))) {
                if (!strcmp(entry->d_name, ".") || !strcmp(entry->d_name, "..")) continue;
                char child[PATH_MAX];
                if (media_plugins_join(child, sizeof child, path, entry->d_name))
                    remove_tree(child);
            }
            closedir(dir);
        }
        rmdir(path);
    } else {
        unlink(path);
    }
}

int main(void) {
    assert(media_plugins_system_dirs[0] != NULL);
    assert(media_plugins_is_plugin("libgstcoreelements.so"));
    assert(media_plugins_is_plugin("libgstnvcodec.so"));
    assert(!media_plugins_is_plugin("libgst.so"));
    assert(!media_plugins_is_plugin("README.txt"));
    assert(!media_plugins_is_plugin("libfoo.so"));
    assert(media_plugins_excluded_name("libgstnvcodec.so"));
    assert(media_plugins_excluded_name("libgstlibav.so"));
    assert(!media_plugins_excluded_name("libgstcoreelements.so"));

    char root_tpl[] = "/tmp/cage-media-plugins-test-XXXXXX";
    char *root = mkdtemp(root_tpl);
    assert(root != NULL);

    char sys_clean[PATH_MAX], sys_nv[PATH_MAX], cache_clean[PATH_MAX], cache_nv[PATH_MAX];
    assert(media_plugins_join(sys_clean, sizeof sys_clean, root, "sys-clean"));
    assert(media_plugins_join(sys_nv, sizeof sys_nv, root, "sys-nv"));
    assert(media_plugins_join(cache_clean, sizeof cache_clean, root, "cache-clean"));
    assert(media_plugins_join(cache_nv, sizeof cache_nv, root, "cache-nv"));
    assert(mkdir(sys_clean, 0700) == 0);
    assert(mkdir(sys_nv, 0700) == 0);
    assert(mkdir(cache_clean, 0700) == 0);
    assert(mkdir(cache_nv, 0700) == 0);

    /* 1. Non-NVIDIA host: no libgstnvcodec.so -> safe no-op. */
    write_dummy(sys_clean, "libgstcoreelements.so");
    write_dummy(sys_clean, "libgstaudioconvert.so");
    clear_gst_env();
    const char *clean_candidates[] = {sys_clean, NULL};
    assert(media_plugins_isolate(cache_clean, clean_candidates) == 0);
    assert(getenv("GST_PLUGIN_SYSTEM_PATH_1_0") == NULL);
    assert(getenv("GST_REGISTRY_1_0") == NULL);
    char gst_clean[PATH_MAX];
    assert(media_plugins_join(gst_clean, sizeof gst_clean, cache_clean, "gstreamer"));
    assert(!path_exists_lstat(gst_clean));

    /* 2. NVIDIA host: libgstnvcodec.so present -> isolate other plugins only. */
    write_dummy(sys_nv, "libgstcoreelements.so");
    write_dummy(sys_nv, "libgstaudioconvert.so");
    write_dummy(sys_nv, "libgstnvcodec.so");
    write_dummy(sys_nv, "notes.txt");
    char bogus_dir[PATH_MAX];
    assert(media_plugins_join(bogus_dir, sizeof bogus_dir, sys_nv, "libgstdir.so"));
    assert(mkdir(bogus_dir, 0700) == 0);

    const char *nv_candidates[] = {sys_nv, NULL};
    clear_gst_env();
    assert(media_plugins_isolate(cache_nv, nv_candidates) == 1);
    const char *plugin_env = getenv("GST_PLUGIN_SYSTEM_PATH_1_0");
    const char *registry_env = getenv("GST_REGISTRY_1_0");
    assert(plugin_env != NULL && strstr(plugin_env, cache_nv) == plugin_env);
    assert(registry_env != NULL && strstr(registry_env, cache_nv) == registry_env);

    char link_core[PATH_MAX], link_audio[PATH_MAX], link_nv[PATH_MAX], link_dir[PATH_MAX], link_notes[PATH_MAX];
    assert(media_plugins_join(link_core, sizeof link_core, plugin_env, "libgstcoreelements.so"));
    assert(media_plugins_join(link_audio, sizeof link_audio, plugin_env, "libgstaudioconvert.so"));
    assert(media_plugins_join(link_nv, sizeof link_nv, plugin_env, "libgstnvcodec.so"));
    assert(media_plugins_join(link_dir, sizeof link_dir, plugin_env, "libgstdir.so"));
    assert(media_plugins_join(link_notes, sizeof link_notes, plugin_env, "notes.txt"));
    assert(path_exists_lstat(link_core));
    assert(path_exists_lstat(link_audio));
    assert(!path_exists_lstat(link_nv));
    assert(!path_exists_lstat(link_dir));
    assert(!path_exists_lstat(link_notes));

    /* 3. Explicit user GStreamer env -> never overridden. */
    clear_gst_env();
    assert(setenv("GST_PLUGIN_SYSTEM_PATH_1_0", "/custom/plugins", 1) == 0);
    assert(media_plugins_isolate(cache_nv, nv_candidates) == 0);
    assert(!strcmp(getenv("GST_PLUGIN_SYSTEM_PATH_1_0"), "/custom/plugins"));
    assert(getenv("GST_REGISTRY_1_0") == NULL);

    /* 4. Re-sync updates added/removed/stale links and rejects non-symlink files. */
    write_dummy(sys_nv, "libgstpulseaudio.so");
    char removed_src[PATH_MAX];
    assert(media_plugins_join(removed_src, sizeof removed_src, sys_nv, "libgstaudioconvert.so"));
    assert(unlink(removed_src) == 0);
    clear_gst_env();
    assert(media_plugins_isolate(cache_nv, nv_candidates) == 1);
    plugin_env = getenv("GST_PLUGIN_SYSTEM_PATH_1_0");
    char link_pulse[PATH_MAX];
    assert(media_plugins_join(link_pulse, sizeof link_pulse, plugin_env, "libgstpulseaudio.so"));
    assert(path_exists_lstat(link_pulse));
    assert(!path_exists_lstat(link_audio));

    write_dummy(plugin_env, "rogue-file");
    clear_gst_env();
    assert(media_plugins_isolate(cache_nv, nv_candidates) == 0);
    assert(getenv("GST_PLUGIN_SYSTEM_PATH_1_0") == NULL);

    /* 5. Symlink root directory is rejected. */
    char cache_sym[PATH_MAX], sym_root[PATH_MAX];
    assert(media_plugins_join(cache_sym, sizeof cache_sym, root, "cache-sym"));
    assert(mkdir(cache_sym, 0700) == 0);
    assert(media_plugins_join(sym_root, sizeof sym_root, cache_sym, "gstreamer"));
    assert(symlink(cache_clean, sym_root) == 0);
    clear_gst_env();
    assert(media_plugins_isolate(cache_sym, nv_candidates) == 0);

    /* 6. DMA-BUF stays enabled without NVIDIA's driver, is disabled with
     *    it, and an explicit caller setting always wins. */
    char gpu_none[PATH_MAX], gpu_proc[PATH_MAX], gpu_sys[PATH_MAX], marker[PATH_MAX];
    assert(media_plugins_join(gpu_none, sizeof gpu_none, root, "gpu-none"));
    assert(media_plugins_join(gpu_proc, sizeof gpu_proc, root, "gpu-proc"));
    assert(media_plugins_join(gpu_sys, sizeof gpu_sys, root, "gpu-sys"));
    assert(mkdir(gpu_none, 0700) == 0 && mkdir(gpu_proc, 0700) == 0 && mkdir(gpu_sys, 0700) == 0);
    make_dirs(gpu_proc, "proc/driver/nvidia");
    assert(media_plugins_join(marker, sizeof marker, gpu_proc, "proc/driver/nvidia"));
    write_dummy(marker, "version");
    make_dirs(gpu_sys, "sys/module/nvidia");
    assert(media_plugins_nvidia_loaded(gpu_none) == 0);
    assert(media_plugins_nvidia_loaded(gpu_proc) == 1);
    assert(media_plugins_nvidia_loaded(gpu_sys) == 1);

    unsetenv("WEBKIT_DISABLE_DMABUF_RENDERER");
    assert(media_plugins_disable_dmabuf_default(gpu_none) == 0);
    assert(getenv("WEBKIT_DISABLE_DMABUF_RENDERER") == NULL);
    assert(media_plugins_disable_dmabuf_default(gpu_proc) == 1);
    assert(!strcmp(getenv("WEBKIT_DISABLE_DMABUF_RENDERER"), "1"));
    assert(media_plugins_disable_dmabuf_default(gpu_proc) == 0);
    unsetenv("WEBKIT_DISABLE_DMABUF_RENDERER");
    assert(media_plugins_disable_dmabuf_default(gpu_sys) == 1);
    assert(setenv("WEBKIT_DISABLE_DMABUF_RENDERER", "0", 1) == 0);
    assert(media_plugins_disable_dmabuf_default(gpu_proc) == 0);
    assert(!strcmp(getenv("WEBKIT_DISABLE_DMABUF_RENDERER"), "0"));
    /* A user without NVIDIA can still ask for it explicitly. */
    assert(setenv("WEBKIT_DISABLE_DMABUF_RENDERER", "1", 1) == 0);
    assert(media_plugins_disable_dmabuf_default(gpu_none) == 0);
    assert(!strcmp(getenv("WEBKIT_DISABLE_DMABUF_RENDERER"), "1"));
    unsetenv("WEBKIT_DISABLE_DMABUF_RENDERER");

    /* 7. gst-libav host: libgstlibav.so alone triggers isolation, is never
     *    linked, and a libav link left by an older private directory is removed. */
    char sys_av[PATH_MAX], cache_av[PATH_MAX];
    assert(media_plugins_join(sys_av, sizeof sys_av, root, "sys-av"));
    assert(media_plugins_join(cache_av, sizeof cache_av, root, "cache-av"));
    assert(mkdir(sys_av, 0700) == 0);
    assert(mkdir(cache_av, 0700) == 0);
    write_dummy(sys_av, "libgstcoreelements.so");
    write_dummy(sys_av, "libgstaudioconvert.so");
    write_dummy(sys_av, "libgstlibav.so");
    const char *av_candidates[] = {sys_av, NULL};
    clear_gst_env();
    assert(media_plugins_isolate(cache_av, av_candidates) == 1);
    plugin_env = getenv("GST_PLUGIN_SYSTEM_PATH_1_0");
    assert(plugin_env != NULL && strstr(plugin_env, cache_av) == plugin_env);
    char av_core[PATH_MAX], av_link[PATH_MAX], av_source[PATH_MAX];
    assert(media_plugins_join(av_core, sizeof av_core, plugin_env, "libgstcoreelements.so"));
    assert(media_plugins_join(av_link, sizeof av_link, plugin_env, "libgstlibav.so"));
    assert(media_plugins_join(av_source, sizeof av_source, sys_av, "libgstlibav.so"));
    assert(path_exists_lstat(av_core));
    assert(!path_exists_lstat(av_link));

    assert(symlink(av_source, av_link) == 0);
    assert(path_exists_lstat(av_link));
    clear_gst_env();
    assert(media_plugins_isolate(cache_av, av_candidates) == 1);
    assert(!path_exists_lstat(av_link));
    assert(path_exists_lstat(av_core));
    clear_gst_env();

    remove_tree(root);
    puts("Linux media-plugins unit tests: all checks passed.");
    return 0;
}

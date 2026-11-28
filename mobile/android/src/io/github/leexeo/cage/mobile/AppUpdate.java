package io.github.leexeo.cage.mobile;

import android.app.Activity;
import android.app.AlertDialog;
import android.app.ProgressDialog;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.net.Uri;
import android.os.Bundle;
import android.provider.Settings;
import android.widget.Toast;
import androidx.core.content.FileProvider;
import cagemobile.AndroidUpdate;
import java.io.File;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;

final class AppUpdate {
    private static final int ALLOW_INSTALL = 43, INSTALL = 44;
    private static final long INTERVAL = TimeUnit.HOURS.toMillis(12);
    private final Activity activity;
    private final SharedPreferences preferences;
    private final ExecutorService worker = Executors.newSingleThreadExecutor();
    private final String current;
    private boolean resumed, checking, manual;
    private Runnable deferred;
    private File ready;
    private String readyVersion;
    private ProgressDialog progress;
    private AlertDialog prompt;

    AppUpdate(Activity activity, Bundle saved) {
        this.activity = activity;
        preferences = activity.getSharedPreferences("app-updates", Activity.MODE_PRIVATE);
        try {
            current = activity.getPackageManager().getPackageInfo(activity.getPackageName(), 0).versionName;
        } catch (PackageManager.NameNotFoundException e) {
            throw new IllegalStateException(e);
        }
        if (saved != null && saved.getString("update-apk", "").matches("[a-f0-9]{64}\\.apk")) {
            ready = new File(new File(activity.getCacheDir(), "updates"), saved.getString("update-apk"));
            readyVersion = saved.getString("update-version");
        }
    }

    String currentVersion() { return "development".equals(current) ? activity.getString(R.string.development_build) : current; }

    void saveState(Bundle state) {
        if (ready != null) {
            state.putString("update-apk", ready.getName());
            state.putString("update-version", readyVersion);
        }
    }

    void pause() { resumed = false; }
    void resume() {
        resumed = true;
        if (ready != null && (current.equals(readyVersion) || !ready.isFile() ||
                System.currentTimeMillis() - ready.lastModified() >= INTERVAL)) ready = null;
        if (deferred != null) {
            Runnable action = deferred;
            deferred = null;
            action.run();
        } else if (ready == null) check(false);
    }

    void check(boolean requested) {
        if ("development".equals(current)) {
            if (requested) message(activity.getString(R.string.development_update));
            return;
        }
        if (progress != null || (prompt != null && prompt.isShowing())) return;
        if (requested && ready != null && ready.isFile()) { offerInstall(); return; }
        if (requested) {
            manual = true;
            message(activity.getString(R.string.checking_updates));
        }
        if (checking) return;
        long now = System.currentTimeMillis();
        long elapsed = now - preferences.getLong("checked-at", 0);
        if (!requested && elapsed >= 0 && elapsed < INTERVAL) return;
        preferences.edit().putLong("checked-at", now).apply();
        checking = true;
        worker.execute(() -> {
            try {
                File[] files = new File(activity.getCacheDir(), "updates").listFiles();
                if (files != null) for (File file : files)
                    if (now - file.lastModified() > TimeUnit.DAYS.toMillis(1)) file.delete();
                AndroidUpdate update = UpdateDownload.check(current);
                deliver(() -> {
                    checking = false;
                    boolean report = manual;
                    manual = false;
                    if (update == null) {
                        if (report) message(activity.getString(R.string.up_to_date));
                        return;
                    }
                    prompt = new AlertDialog.Builder(activity)
                            .setTitle(activity.getString(R.string.new_version, update.version()))
                            .setMessage(activity.getString(R.string.current_version_download, current))
                            .setNegativeButton(activity.getString(R.string.later), null)
                            .setPositiveButton(activity.getString(R.string.download_update), (d, w) -> download(update)).show();
                });
            } catch (Exception e) {
                deliver(() -> {
                    checking = false;
                    if (manual) failure(activity.getString(R.string.update_check_failed), e);
                    manual = false;
                });
            }
        });
    }

    @SuppressWarnings("deprecation")
    private void download(AndroidUpdate update) {
        ProgressDialog dialog = new ProgressDialog(activity);
        progress = dialog;
        progress.setProgressStyle(ProgressDialog.STYLE_HORIZONTAL);
        progress.setTitle(activity.getString(R.string.downloading_version, update.version()));
        progress.setMax(100);
        progress.setCanceledOnTouchOutside(false);
        Future<?> task = worker.submit(() -> {
            try {
                File file = UpdateDownload.fetch(activity, update, value -> activity.runOnUiThread(() -> {
                    if (progress == dialog) dialog.setProgress(value);
                }));
                deliver(() -> {
                    if (progress != dialog) return;
                    finishDownload();
                    ready = file;
                    readyVersion = update.version();
                    install();
                });
            } catch (Exception e) {
                deliver(() -> {
                    if (progress != dialog) return;
                    finishDownload();
                    failure(activity.getString(R.string.update_download_failed), e);
                });
            }
        });
        dialog.setOnCancelListener(d -> {
            task.cancel(true);
            progress = null;
        });
        dialog.setButton(ProgressDialog.BUTTON_NEGATIVE, activity.getString(R.string.cancel), (d, which) -> dialog.cancel());
        dialog.show();
    }

    private void finishDownload() {
        if (progress != null) { progress.dismiss(); progress = null; }
    }

    private void offerInstall() {
        prompt = new AlertDialog.Builder(activity).setTitle(activity.getString(R.string.install_version, readyVersion))
                .setMessage(activity.getString(R.string.update_verified))
                .setNegativeButton(activity.getString(R.string.later), null)
                .setPositiveButton(activity.getString(R.string.continue_install), (d, w) -> install()).show();
    }

    @SuppressWarnings("deprecation")
    private void install() {
        if (ready == null || !ready.isFile()) {
            ready = null;
            message(activity.getString(R.string.update_file_removed));
            return;
        }
        try {
            if (!activity.getPackageManager().canRequestPackageInstalls()) {
                prompt = new AlertDialog.Builder(activity).setTitle(activity.getString(R.string.allow_update_install))
                        .setMessage(activity.getString(R.string.allow_unknown_source))
                        .setNegativeButton(activity.getString(R.string.later), null)
                        .setPositiveButton(activity.getString(R.string.open_settings), (d, w) -> openPermissionSettings()).show();
                return;
            }
            Uri uri = FileProvider.getUriForFile(activity, activity.getPackageName() + ".updates", ready);
            Intent intent = new Intent(Intent.ACTION_INSTALL_PACKAGE, uri)
                    .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION).putExtra(Intent.EXTRA_RETURN_RESULT, true);
            activity.startActivityForResult(intent, INSTALL);
        } catch (RuntimeException e) {
            message(activity.getString(R.string.installer_failed));
        }
    }

    private void openPermissionSettings() {
        try {
            activity.startActivityForResult(new Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
                    Uri.parse("package:" + activity.getPackageName())), ALLOW_INSTALL);
        } catch (RuntimeException e) {
            message(activity.getString(R.string.enable_install_permission));
        }
    }

    boolean onResult(int code, int result) {
        if (code == ALLOW_INSTALL) {
            if (activity.getPackageManager().canRequestPackageInstalls()) install();
            else message(activity.getString(R.string.install_permission_missing));
            return true;
        }
        if (code == INSTALL) {
            if (result == Activity.RESULT_OK) {
                if (ready != null) ready.delete();
                ready = null;
            } else message(activity.getString(R.string.update_not_installed));
            return true;
        }
        return false;
    }

    private void deliver(Runnable action) {
        activity.runOnUiThread(() -> {
            if (activity.isDestroyed() || activity.isFinishing()) return;
            if (resumed) action.run(); else deferred = action;
        });
    }

    private void message(String text) { Toast.makeText(activity, text, Toast.LENGTH_LONG).show(); }

    private void failure(String title, Exception e) {
        String detail = e.getMessage();
        if (e instanceof UpdateDownload.Failure) {
            detail = ((UpdateDownload.Failure) e).detail(activity);
        } else if ("更新源返回了无效的版本信息".equals(detail)) {
            detail = activity.getString(R.string.invalid_update_metadata);
        } else if ("ru".equals(activity.getString(R.string.ui_language))
                || detail == null || !detail.matches("[\\p{IsHan}].{0,100}")) {
            detail = activity.getString(R.string.check_network_retry);
        }
        prompt = new AlertDialog.Builder(activity).setTitle(title).setMessage(detail)
                .setPositiveButton(activity.getString(R.string.got_it), null).show();
    }

    void close() {
        worker.shutdownNow();
        if (prompt != null) prompt.dismiss();
        finishDownload();
        deferred = null;
    }
}

using System.Collections.Concurrent;
using System.Reflection;
using System.Security.Cryptography;
using System.Text.Json;
using Cage.Windows;

internal static class AppUpdaterTests
{
    public static bool BackendFixture(string operation)
    {
        var phase = Environment.GetEnvironmentVariable("CAGE_UPDATER_TEST_PHASE");
        if (phase == null) return false;
        File.AppendAllText(Path.Combine(Backend.Root, "update-requests"), operation + "\n");
        Console.WriteLine(JsonSerializer.Serialize(new {
            phase = operation == "download" ? "failed" : phase,
            latestVersion = "99.0.0", message = "fixture response",
            retryAt = phase == "failed" ? DateTimeOffset.UtcNow.AddHours(1).ToString("O") : "",
        }));
        if (phase == "failed") Environment.Exit(1);
        return true;
    }

    public static async Task Run()
    {
        var root = Path.Combine(Path.GetTempPath(), "cage-updater-test-" + Guid.NewGuid());
        var resources = Path.Combine(AppContext.BaseDirectory, "resources");
        if (Directory.Exists(resources)) throw new IOException("updater fixture requires an unused resources directory");
        var savedProfile = Environment.GetEnvironmentVariable("CLAUDE_CAGE_CONFIG");
        var savedPhase = Environment.GetEnvironmentVariable("CAGE_UPDATER_TEST_PHASE");
        Directory.CreateDirectory(root);
        Directory.CreateDirectory(resources);
        try
        {
            Environment.SetEnvironmentVariable("CLAUDE_CAGE_CONFIG", Path.Combine(root, "config.json"));
            foreach (var file in Directory.EnumerateFiles(AppContext.BaseDirectory))
                File.Copy(file, Path.Combine(resources, Path.GetFileName(file)));
            File.Copy(Environment.ProcessPath!, Backend.Executable, true);
            if (!OperatingSystem.IsWindows()) File.SetUnixFileMode(Backend.Executable, File.GetUnixFileMode(Environment.ProcessPath!));
            var requests = Path.Combine(root, "update-requests");
            var phases = new ConcurrentQueue<string>();
            void Check(bool value, string message) { if (!value) throw new Exception(message); }
            async Task<Dictionary<string, object?>> Exercise(string operation, string backendPhase, Func<string>? publisher, string expectedOperation, Func<bool>? canUpgradeUnsigned = null)
            {
                phases.Clear();
                File.WriteAllText(requests, "");
                Environment.SetEnvironmentVariable("CAGE_UPDATER_TEST_PHASE", backendPhase);
                using var updater = new AppUpdater(s => phases.Enqueue(s.GetValueOrDefault("phase") as string ?? ""),
                    () => throw new Exception("unexpected installation"), () => throw new Exception("unexpected restart"), publisher, null, canUpgradeUnsigned);
                updater.Handle(operation);
                // Wait for both terminal state publication and subprocess cleanup.
                var running = typeof(AppUpdater).GetField("cancellation", BindingFlags.Instance | BindingFlags.NonPublic)!;
                using var deadline = new CancellationTokenSource(TimeSpan.FromSeconds(15));
                while (running.GetValue(updater) != null) await Task.Delay(10, deadline.Token);
                var state = updater.Handle("status");
                Check(File.ReadAllText(requests) == expectedOperation + "\n", "unexpected backend operation");
                if (backendPhase == "failed")
                {
                    Check(!string.IsNullOrEmpty(state.GetValueOrDefault("retryAt") as string), "retry deadline lost");
                    updater.Handle("check"); updater.Handle("install");
                    await Task.Delay(100);
                    Check(File.ReadAllText(requests) == expectedOperation + "\n", "cooldown bypassed");
                }
                return state;
            }

            // On Windows exercise the real WinVerifyTrust path with this unsigned executable.
            // Without Cage.install-id in AppContext.BaseDirectory, default unsigned upgrade is disabled.
            Func<string>? unsigned = OperatingSystem.IsWindows() ? null : () => throw new IOException("unsigned fixture");
            foreach (var operation in new[] { "check", "install" })
            {
                var state = await Exercise(operation, "available", unsigned, "check");
                Check(state["phase"] as string == "manual", "unsigned build did not offer manual update");
                Check(state["latestVersion"] as string == "99.0.0", "manual update lost release version");
                Check(state.GetValueOrDefault("officialUpgrade") as bool? != true, "manual update must not advertise officialUpgrade");
                Check(!phases.Contains("available") && !phases.Contains("downloading"), "unsigned build exposed automatic installation");
                Check((state["message"] as string)?.Contains("官方发布页") == true, "manual update omitted release guidance");
            }
            var untrusted = await Exercise("install", "available", () => throw new CryptographicException("untrusted"), "check");
            Check(untrusted["phase"] as string == "manual", "unverifiable signer allowed a download");
            var current = await Exercise("check", "current", unsigned, "check");
            Check(current["phase"] as string == "current", "current build showed a spurious manual update");
            var unsignedUpgradeCheck = await Exercise("check", "available", unsigned, "check", () => true);
            Check(unsignedUpgradeCheck["phase"] as string == "available", "eligible unsigned build did not offer official upgrade");
            Check(unsignedUpgradeCheck.GetValueOrDefault("officialUpgrade") as bool? == true, "eligible unsigned build did not mark officialUpgrade");
            await Exercise("install", "available", unsigned, "download", () => true);
            var unsignedUpgradeCurrent = await Exercise("check", "current", unsigned, "check", () => true);
            Check(unsignedUpgradeCurrent.GetValueOrDefault("officialUpgrade") as bool? != true, "current phase must clear officialUpgrade");
            var signed = await Exercise("check", "available", () => "fixture-publisher", "check", () => true);
            Check(signed["phase"] as string == "available", "trusted publisher lost automatic update option");
            Check(signed.GetValueOrDefault("officialUpgrade") as bool? != true, "signed build must not mark officialUpgrade");
            await Exercise("install", "available", () => "fixture-publisher", "download", () => true);
            var failed = await Exercise("check", "failed", unsigned, "check");
            Check(failed["phase"] as string == "failed", "network failure was hidden as manual update");
            Console.WriteLine("Windows updater eligibility: unsigned/untrusted manual fallback, eligible official upgrade, trusted download and cooldown passed.");
        }
        finally
        {
            Environment.SetEnvironmentVariable("CLAUDE_CAGE_CONFIG", savedProfile);
            Environment.SetEnvironmentVariable("CAGE_UPDATER_TEST_PHASE", savedPhase);
            Directory.Delete(resources, true);
            Directory.Delete(root, true);
        }
    }
}

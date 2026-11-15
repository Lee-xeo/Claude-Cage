using System.Diagnostics;
using System.Reflection;
using System.Runtime.CompilerServices;
using System.Text.Json.Nodes;

namespace Cage.GuestClipboard;

// Fixed embedded helper under the paired GUI identity. No caller-supplied code,
// executable or path. Reading tool receipts does not prepare account directories.
internal static class CacheMaintenanceRpc
{
    [ModuleInitializer] internal static void Register() => GuestOperations.Register("cache-maintenance", Run);

    private static async Task Run(Stream stream, CancellationToken token, Func<bool> available)
    {
        if (!available()) throw new InvalidDataException("requires_gui_login");
        var request = await Protocol.Read(stream, 32768, token) ?? throw new EndOfStreamException();
        if (request["action"]?.GetValue<string>() is not ("inspect" or "clean" or "restore")) throw new InvalidDataException("cache_operation_invalid");
        using var resource = Assembly.GetExecutingAssembly().GetManifestResourceStream("Cage.CacheMaintenance") ?? throw new InvalidDataException("cache_helper_missing");
        using var reader = new StreamReader(resource, Protocol.Utf8);
        var source = await reader.ReadToEndAsync(token);
        var start = new ProcessStartInfo(WindowsCliRpc.MaintenancePython())
        {
            UseShellExecute = false, CreateNoWindow = true,
            RedirectStandardInput = true, RedirectStandardOutput = true, RedirectStandardError = true,
        };
        foreach (var argument in new[] { "-X", "utf8", "-I", "-c", source }) start.ArgumentList.Add(argument);
        using var process = Process.Start(start) ?? throw new InvalidDataException("cache_helper_failed");
        using var job = new GuestProcessJob(process);
        using var cancellation = token.Register(() => { try { process.Kill(true); } catch { } });
        var errors = process.StandardError.BaseStream.CopyToAsync(Stream.Null, token);
        await process.StandardInput.WriteLineAsync(request.ToJsonString().AsMemory(), token);
        process.StandardInput.Close();
        var output = new byte[65537];
        var length = 0;
        while (length < output.Length)
        {
            var count = await process.StandardOutput.BaseStream.ReadAsync(output.AsMemory(length), token);
            if (count == 0) break;
            length += count;
        }
        if (length > 65536) { process.Kill(true); throw new InvalidDataException("cache_response_too_large"); }
        await process.WaitForExitAsync(token);
        await errors;
        if (!available() || process.ExitCode != 0) throw new InvalidDataException("cache_helper_failed");
        var result = JsonNode.Parse(output.AsSpan(0, length)) as JsonObject ?? throw new InvalidDataException("cache_response_invalid");
        await Protocol.Write(stream, result, token);
    }
}

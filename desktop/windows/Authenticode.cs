using System.Runtime.InteropServices;
using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;

namespace Cage.Windows;

internal static class Authenticode
{
    // Pinned official release publisher identity. Used only when an eligible
    // unsigned source build opts to upgrade in-place to the official signed
    // release; signed installations always pin to their own running signature.
    public const string OfficialPublisherKey = "4D26DB6B8D67E016770E2A8530A4BE81D8D82F5778ED9FC368119B6A3F795839";
    public const string OfficialPublisherOrganization = "CN\nLI SHI HAO";

    private static readonly Lazy<(string Key, string Organization)> Own = new(() => Identity(Environment.ProcessPath!, currentApp: true));

    public static string OwnPublisher() => Identity(Environment.ProcessPath!, currentApp: true).Key;

    public static string Publisher(string path) => Identity(path, currentApp: false).Key;

    // Key identifies one certificate. Organization is the publisher Windows
    // verified in the subject, which stays the same when a certificate is
    // renewed with a new key.
    private static (string Key, string Organization) Identity(string path, bool currentApp)
    {
        UpdatePolicy.PlainPath(path);
        var file = new TrustFile { Size = (uint)Marshal.SizeOf<TrustFile>(), Path = path };
        var memory = Marshal.AllocHGlobal(Marshal.SizeOf<TrustFile>());
        try
        {
            Marshal.StructureToPtr(file, memory, false);
            var data = new TrustData {
                Size = (uint)Marshal.SizeOf<TrustData>(), UiChoice = 2, UnionChoice = 1,
                File = memory, StateAction = 1,
                // Verification must not make a direct network request around the
                // selected Cage proxy. Windows uses its existing trust cache.
                ProviderFlags = 0x1000,
            };
            var action = new Guid("00AAC56B-CD44-11D0-8CC2-00C04FC295EE");
            var result = WinVerifyTrust(new IntPtr(-1), ref action, ref data);
            data.StateAction = 2; _ = WinVerifyTrust(new IntPtr(-1), ref action, ref data);
            if (result != 0)
            {
                var code = $"0x{unchecked((uint)result):X8}";
                if (currentApp)
                    throw new IOException($"当前 Windows 程序签名无法验证（{code}）。");
                throw new IOException($"Windows 无法验证安装包签名（{code}）；原应用已保留。请确认系统证书信任状态。");
            }
#pragma warning disable SYSLIB0057
            using var signed = X509Certificate.CreateFromSignedFile(path);
            using var certificate = new X509Certificate2(signed);
#pragma warning restore SYSLIB0057
            return (Convert.ToHexString(SHA256.HashData(certificate.PublicKey.ExportSubjectPublicKeyInfo())), UpdatePolicy.PublisherOrganization(certificate.SubjectName));
        }
        finally { Marshal.DestroyStructure<TrustFile>(memory); Marshal.FreeHGlobal(memory); }
    }

    // A file matches when it carries the expected certificate key, or when the
    // running App carries that key and Windows trusts the file's signature for
    // the same verified organization. The second case keeps updates working
    // after the signing certificate is renewed.
    public static void SamePublisher(string path, string expected)
    {
        var actual = Identity(path, currentApp: false);
        if (string.Equals(actual.Key, expected, StringComparison.Ordinal)) return;
        if (string.Equals(expected, OfficialPublisherKey, StringComparison.Ordinal) &&
            string.Equals(actual.Organization, OfficialPublisherOrganization, StringComparison.Ordinal)) return;
        try
        {
            var own = Own.Value;
            if (string.Equals(own.Key, expected, StringComparison.Ordinal) && own.Organization.Length != 0 &&
                string.Equals(actual.Organization, own.Organization, StringComparison.Ordinal)) return;
        }
        catch (Exception error) when (error is IOException or CryptographicException or UnauthorizedAccessException) { }
        throw new IOException("更新包的发布者签名与当前 Cage 不一致");
    }

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct TrustFile { public uint Size; [MarshalAs(UnmanagedType.LPWStr)] public string Path; public IntPtr Handle; public IntPtr KnownSubject; }
    [StructLayout(LayoutKind.Sequential)]
    private struct TrustData {
        public uint Size; public IntPtr PolicyCallback, SipClient;
        public uint UiChoice, RevocationChecks, UnionChoice; public IntPtr File;
        public uint StateAction; public IntPtr StateData, UrlReference;
        public uint ProviderFlags, UiContext; public IntPtr SignatureSettings;
    }
    [DllImport("wintrust.dll", ExactSpelling = true, PreserveSig = true)]
    private static extern int WinVerifyTrust(IntPtr window, ref Guid action, ref TrustData data);
}

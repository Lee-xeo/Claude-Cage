#import "macos-guest-identity.h"
#include <fcntl.h>
#include <unistd.h>
#include <termios.h>
#include <poll.h>
#include <errno.h>
#include <sys/ioctl.h>
#include <CommonCrypto/CommonDigest.h>

bool cageGuestIsVirtualMac(void) { return cageIsVirtualMac(); }

#ifndef CAGE_GUEST_IDENTITY_LIBRARY
static int cageVerifyInstallImage(const char *path, const char *sizeText, const char *digest) {
    if (geteuid() != 0 || !cageIntelVirtualHardware() || strlen(digest) != 64) return 72;
    char *end = NULL;
    unsigned long long expected = strtoull(sizeText, &end, 10);
    if (!expected || !end || *end) return 72;
    for (size_t i = 0; i < 64; ++i) if (!((digest[i] >= '0' && digest[i] <= '9') || (digest[i] >= 'a' && digest[i] <= 'f'))) return 72;
    int fd = open(path, O_RDONLY|O_NOFOLLOW);
    struct stat info;
    if (fd < 0) return 72;
    if (fstat(fd, &info) != 0 || !S_ISREG(info.st_mode) || (unsigned long long)info.st_size != expected) { close(fd); return 72; }
    CC_SHA256_CTX hash;
    CC_SHA256_Init(&hash);
    unsigned char buffer[1024*1024], result[CC_SHA256_DIGEST_LENGTH];
    ssize_t count;
    while ((count = read(fd, buffer, sizeof(buffer))) > 0) CC_SHA256_Update(&hash, buffer, (CC_LONG)count);
    close(fd);
    if (count < 0) return 72;
    CC_SHA256_Final(result, &hash);
    char actual[65];
    for (size_t i = 0; i < sizeof(result); ++i) snprintf(actual+i*2, 3, "%02x", result[i]);
    return strcmp(actual, digest) == 0 ? 0 : 72;
}

static int cageWriteSerial(NSData *output) {
    int fd = open("/dev/cu.serial1", O_WRONLY|O_NOCTTY|O_NONBLOCK);
    if (fd < 0) return 73;
    struct termios settings;
    if (tcgetattr(fd, &settings) != 0) { close(fd); return 73; }
    cfmakeraw(&settings);
    settings.c_cflag |= CLOCAL|CREAD;
    settings.c_cflag &= ~(CCTS_OFLOW|CRTS_IFLOW|HUPCL);
    cfsetspeed(&settings, B115200);
    if (tcsetattr(fd, TCSANOW, &settings) != 0) { close(fd); return 73; }
    size_t sent = 0;
    for (int attempt = 0; attempt < 50 && sent < output.length; ++attempt) {
        ssize_t count = write(fd, (const char *)output.bytes + sent, output.length - sent);
        if (count > 0) sent += count;
        else if (errno != EAGAIN && errno != EINTR) break;
        struct pollfd wait = {fd, POLLOUT, 0};
        poll(&wait, 1, 100);
    }
    BOOL drained = NO;
    if (sent == output.length) {
        // POLLOUT only promises queue space. Closing immediately can discard
        // the host-key line before the UART has transmitted it.
        for (int attempt = 0; attempt < 100; ++attempt) {
            int pending = 0;
            if (ioctl(fd, TIOCOUTQ, &pending) != 0) break;
            if (pending == 0) { usleep(20000); drained = YES; break; }
            usleep(10000);
        }
    }
    close(fd);
    return drained ? 0 : 73;
}

int main(int argc, const char **argv) {
    @autoreleasepool {
        if (argc == 5 && strcmp(argv[1], "--verify-install-image") == 0) return cageVerifyInstallImage(argv[2], argv[3], argv[4]);
        if (argc == 3 && strcmp(argv[1], "--install-stage") == 0 && geteuid() == 0 && cageIntelVirtualHardware()) {
            NSDictionary *stages = @{@"wait":@"WAIT_DISKS", @"prepare":@"PREPARE", @"start":@"START", @"error":@"ERROR"};
            NSString *stage = stages[[NSString stringWithUTF8String:argv[2]]];
            if (!stage) return 72;
            return cageWriteSerial([[NSString stringWithFormat:@"\nCAGE_INTEL_INSTALL_%@\n",stage] dataUsingEncoding:NSUTF8StringEncoding]);
        }
        if (argc == 2 && strcmp(argv[1], "--publish-host-key") == 0 && geteuid() == 0 && cageIntelIdentity()) {
            NSString *path = @"/etc/ssh/ssh_host_ed25519_key.pub";
            if (!cageRootRegular(path)) return 72;
            NSString *key = [[NSString alloc] initWithData:[NSData dataWithContentsOfFile:path] encoding:NSUTF8StringEncoding];
            if (![key hasPrefix:@"ssh-ed25519 "] || key.length > 512) return 72;
            NSData *line = [[@"\nCAGE-GUEST-HOST-KEY " stringByAppendingString:[key stringByTrimmingCharactersInSet:NSCharacterSet.newlineCharacterSet]] dataUsingEncoding:NSUTF8StringEncoding];
            NSMutableData *output = [line mutableCopy];
            [output appendBytes:"\n" length:1];
            return cageWriteSerial(output);
        }
        if (argc == 2 && strcmp(argv[1], "--hardware-uuid") == 0 && cageIntelVirtualHardware()) {
            NSString *uuid = cagePlatformUUID();
            if (!uuid) return 72;
            puts(uuid.UTF8String);
            return 0;
        }
        if (argc == 2 && strcmp(argv[1], "--check") == 0) return cageIsVirtualMac() ? 0 : 72;
        if (argc == 3 && strcmp(argv[1], "--expect") == 0) {
            NSDictionary *identity = cageIntelIdentity();
            return identity && [identity[@"uuid"] isEqual:[NSString stringWithUTF8String:argv[2]].lowercaseString] ? 0 : 72;
        }
        if (argc != 4 || strcmp(argv[1], "--prepare") != 0 || geteuid() != 0 || !cageIntelVirtualHardware()) return 72;
        NSString *uuid = [[NSUUID alloc] initWithUUIDString:[NSString stringWithUTF8String:argv[2]]].UUIDString.lowercaseString;
        NSString *installation = [[NSUUID alloc] initWithUUIDString:[NSString stringWithUTF8String:argv[3]]].UUIDString.lowercaseString;
        if (!uuid || !installation || ![uuid isEqual:cagePlatformUUID()]) return 72;
        NSDictionary *receipt = @{@"version":@1, @"backend":@"qemu-hvf", @"uuid":uuid, @"installation":installation};
        if ([[NSFileManager defaultManager] fileExistsAtPath:cageIdentityPath]) return [cageIntelIdentity() isEqual:receipt] ? 0 : 72;
        NSData *data = [NSJSONSerialization dataWithJSONObject:receipt options:NSJSONWritingSortedKeys error:NULL];
        NSString *temporary = [cageIdentityPath stringByAppendingFormat:@".%@",NSUUID.UUID.UUIDString];
        int fd = open(temporary.fileSystemRepresentation,O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW,0644);
        if (fd < 0) return 72;
        BOOL written = write(fd,data.bytes,data.length) == (ssize_t)data.length && fsync(fd) == 0;
        close(fd);
        BOOL published = written && link(temporary.fileSystemRepresentation,cageIdentityPath.fileSystemRepresentation) == 0;
        unlink(temporary.fileSystemRepresentation);
        return published ? 0 : 72;
    }
}
#endif

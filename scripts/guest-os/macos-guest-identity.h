#import <Foundation/Foundation.h>
#import <IOKit/IOKitLib.h>
#include <sys/stat.h>
#include <sys/sysctl.h>
#include <stdbool.h>

static NSString *const cageIdentityPath = @"/var/db/cage-intel-macos.json";
static BOOL cageRootRegular(NSString *path) {
    struct stat st;
    return lstat(path.fileSystemRepresentation, &st) == 0 && S_ISREG(st.st_mode) &&
        st.st_uid == 0 && !(st.st_mode & 022) && st.st_size > 0 && st.st_size <= 4096;
}
static NSString *cagePlatformUUID(void) {
    // Intel macOS hashes system-id before publishing IOPlatformUUID. Bind to
    // the original firmware UUID shared by OpenCore and QEMU instead.
    io_registry_entry_t service = IORegistryEntryFromPath(kIOMainPortDefault, "IODeviceTree:/efi/platform");
    if (!service) return nil;
    CFTypeRef raw = IORegistryEntryCreateCFProperty(service, CFSTR("system-id"), kCFAllocatorDefault, 0);
    IOObjectRelease(service);
    NSString *value = nil;
    if (raw && CFGetTypeID(raw) == CFDataGetTypeID() && CFDataGetLength(raw) == 16)
        value = [[NSUUID alloc] initWithUUIDBytes:CFDataGetBytePtr(raw)].UUIDString.lowercaseString;
    if (raw) CFRelease(raw);
    return value;
}
static BOOL cageIntelVirtualHardware(void) {
    int virtualized = 0; size_t size = sizeof(virtualized);
    char machine[32] = {0}; size_t count = sizeof(machine);
    return sysctlbyname("kern.hv_vmm_present", &virtualized, &size, NULL, 0) == 0 && virtualized == 1 &&
        sysctlbyname("hw.machine", machine, &count, NULL, 0) == 0 && strcmp(machine, "x86_64") == 0;
}
static NSDictionary *cageIntelIdentity(void) {
    if (!cageRootRegular(cageIdentityPath) || !cageIntelVirtualHardware()) return nil;
    NSDictionary *receipt = [NSJSONSerialization JSONObjectWithData:[NSData dataWithContentsOfFile:cageIdentityPath] options:0 error:NULL];
    if (![receipt isKindOfClass:NSDictionary.class] || ![receipt[@"version"] isEqual:@1] ||
        ![receipt[@"backend"] isEqual:@"qemu-hvf"] || ![receipt[@"uuid"] isEqual:cagePlatformUUID()] ||
        ![[NSUUID alloc] initWithUUIDString:receipt[@"installation"]]) return nil;
    return receipt;
}
static BOOL cageIsVirtualMac(void) {
    char model[128] = {0}; size_t size = sizeof(model);
    if (sysctlbyname("hw.model", model, &size, NULL, 0) == 0 &&
        [[NSString stringWithUTF8String:model] rangeOfString:@"^VirtualMac[0-9]+,[0-9]+$" options:NSRegularExpressionSearch].location != NSNotFound) return YES;
    return cageIntelIdentity() != nil;
}

//go:build darwin && cgo

#import <Foundation/Foundation.h>

long long cageImportantDiskCapacity(const char *path) {
    @autoreleasepool {
        NSURL *url = [NSURL fileURLWithFileSystemRepresentation:path isDirectory:YES relativeToURL:nil];
        NSNumber *capacity = nil;
        if (![url getResourceValue:&capacity forKey:NSURLVolumeAvailableCapacityForImportantUsageKey error:NULL] ||
            ![capacity isKindOfClass:[NSNumber class]] || capacity.longLongValue < 0) {
            return -1;
        }
        return capacity.longLongValue;
    }
}

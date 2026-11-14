use strict;
use warnings;
use File::Temp qw(tempdir);
use POSIX qw(_exit WNOHANG);
use Socket qw(AF_UNIX SOCK_STREAM PF_UNSPEC);

my $server_readonly = @ARGV == 1 && $ARGV[0] eq 'server-readonly';
die "invalid probe mode" if @ARGV && !$server_readonly;

# Two child processes connected only to one another. A local, read-only SFTP
# endpoint substitutes for the project transport and no host files are shared.
umask 077;
my $tmp = tempdir('cage-check-XXXXXXXX', DIR => '/private/var/tmp', CLEANUP => 0);
my $source = "$tmp/source";
my $mount = "$tmp/mount";
my @children;
my $attached = '';
sub mounted_line {
    open my $list, '-|', '/sbin/mount' or die "mount list";
    my @matches = grep { index($_, " on $mount (") >= 0 } <$list>;
    close $list or die "mount list";
    die "ambiguous mount" if @matches > 1;
    return $matches[0] // '';
}
sub spawn_peer {
    my ($side, $other, @cmd) = @_;
    my $pid = fork();
    die "fork" unless defined $pid;
    return $pid if $pid;
    close $other;
    open STDIN, '<&', $side or _exit(125);
    open STDOUT, '>&', $side or _exit(125);
    open STDERR, '>', '/dev/null' or _exit(125);
    close $side;
    exec @cmd or _exit(126);
}
my $ok = eval {
    local $SIG{TERM} = local $SIG{HUP} = local $SIG{INT} = sub { die "cancelled" };
    mkdir $source, 0700 or die "source";
    mkdir $mount, 0700 or die "target";
    open my $fixture, '>', "$source/readonly" or die "fixture";
    print {$fixture} 'cage-readonly-probe' or die "fixture";
    close $fixture or die "fixture";
    socketpair(my $a,my $b,AF_UNIX,SOCK_STREAM,PF_UNSPEC) or die "socketpair";
    push @children, spawn_peer($a,$b,'/usr/libexec/sftp-server','-R','-d',$source);
    # FSKit before macOS 26.4 cannot advertise a read-only mount. The SFTP
    # endpoint stays -R in both modes and remains the authority for writes.
    my $options = 'slave,'.($server_readonly ? '' : 'ro,').'quiet,backend=fskit,nodev,nosuid,dir_cache=no';
    push @children, spawn_peer($b,$a,'/usr/local/libexec/cage-sshfs',":$source",$mount,'-f','-o',$options);
    close $a; close $b;
    for (1..35) {
        $attached = mounted_line();
        last if $attached;
        last unless kill 0, $children[1];
        select undef,undef,undef,0.2;
    }
    my $user = getpwuid($<);
    my ($flags) = $attached =~ m{^(?:macfuse://[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}|/dev/disk[0-9]+s[0-9]+) on \Q$mount\E \(macfuse, ([^\n]+)\)\s*$}i;
    my %options = map { $_ => 1 } split /, /, ($flags // '');
    die "unexpected mount" unless $options{'fskit'} && $options{'nodev'} && $options{'nosuid'} && ($server_readonly || $options{'read-only'}) && $options{"mounted by $user"};
    open my $read, '<', "$mount/readonly" or die "read-only mount unreadable";
    my $content = do { local $/; <$read> };
    close $read or die "read-only mount unreadable";
    die "wrong read-only source" unless $content eq 'cage-readonly-probe';
    if (open my $write, '>', "$mount/forbidden") {
        close $write;
        die "read-only endpoint accepted a write";
    }
    die "read-only source was modified" if -e "$source/forbidden";
    1;
};
# Never unmount a replacement or remove a mounted tree.
my $live = mounted_line();
if ($live && $live eq $attached) {
    $ok = 0 unless system('/sbin/umount','-f',$mount) == 0;
}
for my $pid (@children) {
    if (waitpid($pid,WNOHANG)==0) {kill 'TERM',$pid}
}
select undef,undef,undef,0.15;
for my $pid (@children) {
    if (waitpid($pid,WNOHANG)==0) {kill 'KILL',$pid;waitpid($pid,0)}
}
$ok = 0 if mounted_line();
$ok = 0 unless rmdir $mount;
unlink "$source/forbidden" if -e "$source/forbidden";
$ok = 0 unless unlink "$source/readonly";
$ok = 0 unless rmdir $source;
$ok = 0 unless rmdir $tmp;
exit($ok ? 0 : 1);

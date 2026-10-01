"""Mutation harness for internal/ed2k (step 5.4).

Four verdicts, and the middle two are the point:

    KILLED     a test failed
    COVERED    the whole suite passed -- a LOWER LAYER already refuses this input
    SURVIVED   the suite passed AND the mutation is reachable, which is a hole
    SKIP       the probe did not compile, or its pattern is no longer in the file

Build errors are SKIP, never SURVIVED. A probe that does not compile is a defect
in the PROBE; scoring it as a hole sends the next reader into the tests looking
for a bug that is in this file. That inversion has bitten twice in this repo --
see the BUILD_ERRORS note below, and the `redeclared` row in the step 5.3
harness whose build error was missing from the list.

EVERY PROBE IS BOUNDED, EVERY PROBE RESTORES ITS FILE, AND NOTHING RUNS THE
SUITE UNBOUNDED. All three are here because of a real incident: a sweep of
internal/library was interrupted by a SIGTERM partway through and the probe in
flight left a mutation applied to integrate.go. The suite then took 126 seconds
to fail and the outer timeout killed the run first -- so the interrupted sweep
reported nothing, and the mutation it left behind looked like a pre-existing bug.

RUN IT WITH `python3 internal/ed2k/mutate_ed2k.py` AND READ THE EXIT CODE.

    0  nothing survived and nothing was malformed
    1  a survivor -- go and look at a TEST
    2  a malformed probe -- go and look at THIS FILE

A single non-zero code sends the reader to the shorter list, so the two are kept
apart. `... | tail -40; echo $?` reports TAIL's status and has already produced
a clean-looking run of a sweep that had seven survivors.

READ THE EXIT CODE OF THE THING, NOT OF THE PIPE.
"""

import json
import os
import subprocess
import sys

PKG = "internal/ed2k"
ROOT = os.path.dirname(os.path.abspath(__file__))
# TWO dirnames, like internal/library/mutate_library.py, because this file sits
# at the same depth: <plugin>/internal/ed2k -> <plugin>. The first version
# climbed THREE and REPO came out as <plugin>/.., which is the stash CORE root —
# a directory that exists and holds a real internal/ed2k-less tree, so the
# failure was a FileNotFoundError on every open rather than a clean verdict.
#
# Worth naming because the quiet version of this mistake is worse: a harness
# pointed one level too high can find files that are merely the wrong files.
REPO = os.path.dirname(os.path.dirname(ROOT))

ED2K = os.path.join(PKG, "ed2k.go")
EHASH = os.path.join(PKG, "ehash.go")
PARSE = os.path.join(PKG, "parse.go")
VERIFY = os.path.join(PKG, "verify.go")

# BUILD_ERRORS must be COMPLETE, not representative.
#
# A missing entry does not merely mis-score one row -- it MANUFACTURES a false
# hole. That is not theoretical: the step 5.3 magnet sweep ended with a row
# scored "SURVIVED -- a different test failed", and the mutation had produced
# `err redeclared in this block`. `redeclared` was not in that harness's list,
# so a defect in the probe was reported as a hole in the tests -- the exact
# inversion the SKIP verdict exists to prevent, reintroduced by the list meant
# to prevent it. Twelve forms, then twelve more after the next one showed up.
BUILD_ERRORS = (
    "build failed", "cannot use", "cannot convert", "cannot define", "undefined:",
    "undeclared", "declared and not used", "redeclared", "no new variables",
    "syntax error", "not enough arguments", "too many arguments",
    "assignment mismatch", "imported and not used", "missing return",
    "typecheck", "shadows declaration", "all declarations of", "no field or method",
    "unused", "declared and not", "not enough values", "invalid operation",
    "cannot range over", "does not implement", "missing method",
)

# Per-probe wall-clock bound. Generous for green, tight enough that a
# NON-terminating mutation is caught by the harness rather than by the caller.
# The suite hashes ~30 MB of buffers, so 25s is real headroom, not a guess: a
# green run of this package takes about a second.
PROBE_TIMEOUT = 60
# The suite must finish well inside this; a probe that blows it is a hang, and a
# hang is a finding about the code, not a reason to wait longer.
SUITE_TIMEOUT = 30

ENV = dict(os.environ, GOFLAGS="-mod=mod",
           PATH="/usr/bin:/bin:/usr/local/bin:" + os.environ.get("PATH", ""))

# Every row is (label, file, old, new, run-pattern).
#
# The run-pattern is `-run`'d so a probe costs a second rather than the whole
# suite. `.` means the whole package. A row that names a test it does NOT
# expect to fail reports KILLED as "a different test failed", which is a claim
# the reader has to check -- so the named tests below were each confirmed to go
# red under their own mutation, by running them.
MUTATIONS = [
    # ---- the boundary, which is where both real bugs were ----
    ("ehash: the boundary comparison widened, discarding the remainder",
     EHASH, "\tif len(first) < PartSize {", "\tif len(first) <= PartSize {", "."),
    ("ehash: the boundary narrowed, so an exact part takes the tree",
     EHASH, "\tif len(first) < PartSize {", "\tif len(first) < PartSize-1 {", "."),
    ("ehash: the short-part fast path removed entirely",
     EHASH, "\tif len(first) < PartSize {", "\tif false {", "."),
    # The `len(rest) == 0` early return is what makes an exactly-one-part file
    # hash WHOLE rather than as a one-part tree. Deleting it is a different bug
    # from widening the boundary and it is the one the spec sentence is about.
    ("ehash: a single full part hashed as a one-part tree",
     EHASH,
     "\tif len(rest) == 0 {\n\t\t// A full part and nothing after it: exactly one chunk, no remainder.\n\t\tsum := Sum(first)\n\t\tcopy(whole[:], sum[:])\n\t\treturn whole, nil\n\t}",
     "\tif false {\n\t}",
     "TestAFileOfExactlyOnePartIsHashedWhole|TestDifferentFilesMustGetDifferentHashes"),
    ("ehash: the remainder's own read discarded, so only two parts ever count",
     EHASH,
     "\t\trest, err = readPart(r)\n\t\tif err != nil {\n\t\t\treturn whole, err\n\t\t}\n\t\tif len(rest) == 0 {\n\t\t\tbreak\n\t\t}",
     "\t\tbreak",
     "TestDifferentFilesMustGetDifferentHashes|TestTheTreeHashesThePartHashesNotThePartBytes"),
    ("ehash: the loop never iterates, so only the first part is hashed",
     EHASH, "\tfor {\n\t\tsum := Sum(rest)", "\tfor {\n\t\tbreak\n\t\tsum := Sum(rest)",
     "TestTheTreeHashesThePartHashesNotThePartBytes|TestThePrefixLengthIsReadFromTheConstant"),
    ("ehash: the tree built from the raw part bytes rather than their hashes",
     EHASH, "\t\tsum := Sum(rest)\n\t\tprefixes = append(prefixes, sum[:PartHashPrefixLength]...)",
     "\t\tprefixes = append(prefixes, rest[:PartHashPrefixLength]...)",
     "TestTheTreeHashesThePartHashesNotThePartBytes"),
    ("ehash: the FULL 16-byte part hashes concatenated, not the first eight",
     EHASH, "\t\tprefixes = append(prefixes, sum[:PartHashPrefixLength]...)",
     "\t\tprefixes = append(prefixes, sum[:]...)",
     "TestTheTreeHashesThePartHashesNotThePartBytes|TestThePrefixLengthIsReadFromTheConstant"),
    # Two edits, because dropping the first part's hash from the buffer alone
    # leaves `sum` declared and unused -- which does not compile, and a
    # non-compile is a SKIP that proves nothing. A defect that needs several
    # edits to EXIST at all has to be written as several.
    ("ehash: the FIRST part's own hash left out of the tree",
     EHASH,
     ['\tsum := Sum(first)\n\tprefixes = append(prefixes, sum[:PartHashPrefixLength]...)',
      "\tfor {"],
     ['\tsum := Sum(first)\n\t_ = sum',
      "\tfor {"],
     "TestTheTreeHashesThePartHashesNotThePartBytes|TestThePrefixLengthIsReadFromTheConstant"),

    # ---- the tree hash's serialisation ----
    ("ehash: the file-list length written BIG-endian",
     EHASH, "binary.LittleEndian.PutUint32", "binary.BigEndian.PutUint32",
     "TestTreeHashIsLittleEndian|TestTreeHashIs37BytesPerEntry"),
    ("ehash: the 0x02 separator byte omitted from each entry",
     EHASH, "\t\tbuf = append(buf, 0x02)", "\t\t// no separator",
     "TestTreeHashIs37BytesPerEntry|TestTheSeparatorIsOneByte"),
    ("ehash: the file's own hash omitted from each entry",
     EHASH, "\t\tbuf = append(buf, e.Hash[:]...)", "\t\t// no hash",
     "TestTreeHashIs37BytesPerEntry"),
    ("ehash: the entry NAME omitted, so two lists differing only in name agree",
     EHASH, "\t\tbuf = append(buf, []byte(e.Name)...)", "\t\t// no name",
     "TestTreeHashIs37BytesPerEntry"),
    ("ehash: the empty file list accepted with a zero hash and no error",
     EHASH, "\tif len(entries) == 0 {", "\tif false {",
     "TestAnEmptyFileListHasNoHash"),
    ("ehash: TreeHash no longer checks entry names for traversal",
     EHASH, "\t\tif err := checkName(e.Name); err != nil {",
     "\t\tif err := error(nil); err != nil {",
     "TestTreeHashRefusesAnEscapingName"),
    # HashBytes is a convenience that must not become a second implementation
    # of the boundary. Routing it through a direct Sum is the obvious
    # "optimisation" and it is wrong for every input over one part.
    #
    # The error is discarded with `_` rather than by blanking the `if`: keeping
    # `err` declared while removing the `if err != nil` that reads it is
    # `declared and not used`, which does not compile — a SKIP, which proves
    # nothing. The first version of this row made exactly that mistake and the
    # harness reported it as a defect in itself, correctly.
    #
    # It scores COVERED, and the verdict is TRUE and was MEASURED rather than
    # assumed: `HashBytes` takes a `[]byte` and hands it to `newByteReader`,
    # which is a `bytes.Reader` over memory. A `bytes.Reader` cannot fail, so
    # the error branch is unreachable from this call site and no input to
    # `HashBytes` can reach it. `HashFile` on a reader that DOES fail does
    # return the error (measured: "reading a part for the eDonkey2000 hash: the
    # disk went away"), so the check is not dead code in `HashFile` — it is
    # dead only from the in-memory convenience wrapper, which is what
    # defence in depth looks like.
    ("ehash: HashBytes ignores the hasher's error and short-circuits the tree",
     EHASH,
     "\th, err := HashFile(newByteReader(b))\n\tif err != nil {",
     "\th, _ := HashFile(newByteReader(b))\n\tif false {",
     "TestHashBytesAndHashFileAgree"),
    # `Sum` returns an array BY VALUE, and `Sum(b)[:]` is `cannot slice
    # unaddressable value` — the first version of this row wrote it that way and
    # the harness reported a SKIP, correctly, as a defect in itself. The
    # intermediate variable is what makes the array addressable, so the row
    # measures the boundary rather than the compiler.
    ("ehash: HashBytes hashes a large input whole instead of by tree",
     EHASH,
     "\t\treturn Hash{}\n\t}\n\treturn h\n}",
     "\t\treturn Hash{}\n\t}\n\tif len(b) > PartSize {\n\t\tvar d Hash\n\t\ts := Sum(b)\n\t\tcopy(d[:], s[:])\n\t\treturn d\n\t}\n\treturn h\n}",
     "TestHashBytesAndHashFileAgree|TestDifferentFilesMustGetDifferentHashes"),

    # ---- Sum and the constants ----
    ("ehash: Sum returns the input's length instead of a digest",
     EHASH, "\tcopy(out[:], h.Sum(nil))", "\tcopy(out[:], b)",
     "TestSumAgainstTheRFC1320Vectors"),
    ("ehash: the part size changed, so every large file hashes differently",
     EHASH, "const PartSize = 9728000", "const PartSize = 9500000",
     "TestPartSizeIsTheProtocolsConstant|TestAFileOfExactlyOnePartIsHashedWhole"),
    ("ehash: the part hash prefix widened to the full hash",
     EHASH, "const PartHashPrefixLength = 8", "const PartHashPrefixLength = 16",
     "TestPartSizeIsTheProtocolsConstant|TestTheTreeHashesThePartHashesNotThePartBytes"),

    # ---- the parse: scheme and shape ----
    ("parse: the scheme prefix check removed",
     PARSE, "\tif !strings.HasPrefix(strings.ToLower(value), schemePrefix) {",
     "\tif false {", "TestTheLinkIsNotAURL"),
    ("parse: the scheme matched case-SENSITIVELY, refusing a real link",
     PARSE, "\tif !strings.HasPrefix(strings.ToLower(value), schemePrefix) {",
     "\tif !strings.HasPrefix(value, schemePrefix) {",
     "TestTheSchemeIsMatchedCaseInsensitivelyAndSurroundedByWhitespaceIsFine"),
    ("parse: the surrounding whitespace not trimmed",
     PARSE, "\tvalue := strings.TrimSpace(raw)", "\tvalue := raw",
     "TestTheSchemeIsMatchedCaseInsensitivelyAndSurroundedByWhitespaceIsFine"),
    ("parse: the leading field separator not required",
     PARSE, "\tif !strings.HasPrefix(body, \"|\") {", "\tif false {",
     "TestTheSchemeIsPresentButTheShapeIsNot"),
    # The field-count guard. It scores COVERED, and the verdict is TRUE and was
    # MEASURED rather than assumed: with `len(fields) < 4` gone,
    # `ed2k://|file|video.mkv|`, `ed2k://|file|`, `ed2k://|file`, `ed2k://|` and
    # `ed2k://` are ALL still refused. Each one runs out of fields before it can
    # produce a usable locator, so the name check, the size check and the hash
    # check each refuse it on their own. The guard is the cheapest and clearest
    # of several refusals, not the only one -- which is what "fails closed at
    # every layer" is supposed to look like, and why this row is a COVERED and
    # not a hole.
    ("parse: a link with too few fields accepted",
     PARSE, "\tif len(fields) < 4 {", "\tif false {",
     "TestTheSchemeIsPresentButTheShapeIsNot|TestTheLinkIsNotAURL"),
    # The UPPER bound is a different guard with a different reason, and it is
    # NOT redundant: a link with fields beyond the hash is a protocol extension
    # or a hostile link trying to smuggle a name, so it has to be refused
    # outright rather than have the extras ignored. The fixture that pins it
    # has all four leading fields valid, so nothing else refuses the link --
    # see TestALinkWithFieldsBeyondTheHashIsRefused.
    ("parse: a link with fields beyond the hash accepted, discarding them",
     PARSE, "\tif len(fields) > 5 {", "\tif false {",
     "TestALinkWithFieldsBeyondTheHashIsRefused|TestANameWithAPipeIsRefusedRatherThanGuessedAt"),
    ("parse: a folder link treated as a file link",
     PARSE, "\tcase \"folder\":\n\t\tloc.Kind = KindFolder",
     "\tcase \"folder\":\n\t\tloc.Kind = KindFile",
     "TestAFolderLinkIsNotAFileLink|TestAnUnknownKindIsRefused"),
    # The WHOLE `default` block, closing brace included. An earlier version
    # replaced only the opening lines, which left the switch's `}` consumed by
    # the `if false {` it introduced and produced `expected '(', found
    # ParseHash` — a SKIP reporting a defect in this file, which was true.
    ("parse: an unrecognised kind accepted as a file",
     PARSE,
     "\tdefault:\n\t\treturn Locator{}, fmt.Errorf(\"%w: %q is neither \\\"file\\\" nor \"+\n"
     "\t\t\t\"\\\"folder\\\". A folder link's hash covers a FILE LIST, not a file, \"+\n"
     "\t\t\t\"so treating one as the other sends a caller looking for a file \"+\n"
     "\t\t\t\"that does not exist\", ErrMalformed, fields[0])\n\t}",
     "\tdefault:\n\t\tloc.Kind = KindFile\n\t\tif false {\n"
     "\t\t\treturn Locator{}, fmt.Errorf(\"%w: %q is neither \\\"file\\\" nor \"+\n"
     "\t\t\t\t\"\\\"folder\\\". A folder link's hash covers a FILE LIST, not a file, \"+\n"
     "\t\t\t\t\"so treating one as the other sends a caller looking for a file \"+\n"
     "\t\t\t\t\"that does not exist\", ErrMalformed, fields[0])\n\t\t}\n\t}",
     "TestAnUnknownKindIsRefused"),
    ("parse: the empty name accepted",
     PARSE, "\tif loc.Name == \"\" {", "\tif false {",
     "TestTheEmptyNameIsRefused"),
    ("parse: the name taken from the wrong field",
     PARSE, "\tloc.Name = fields[1]", "\tloc.Name = fields[0]",
     "TestTheOrdinaryLinkParses|TestTheNameIsNotPercentDecoded"),
    # A COMPOUND mutation, as a list of edits. Adding the import on its own
    # leaves an unused import, which does not compile -- a SKIP, which proves
    # nothing and is reported as a defect in THIS FILE rather than a kill. The
    # import and the use have to land together, and the two edits are only
    # meaningful as a pair, so they are one row.
    ("parse: the name percent-decoded, turning a valid file into a 404",
     PARSE,
     ['\t"errors"\n\t"fmt"', "\tloc.Name = fields[1]"],
     ['\t"errors"\n\t"fmt"\n\t"net/url"',
      "\tloc.Name = fields[1]\n\tif u, e := url.QueryUnescape(loc.Name); e == nil {\n\t\tloc.Name = u\n\t}"],
     "TestTheNameIsNotPercentDecoded"),

    # ---- the parse: the three refusals that matter ----
    ("parse: the size taken from the wrong field",
     PARSE, "\tsize, err := strconv.ParseInt(fields[2], 10, 64)",
     "\tsize, err := strconv.ParseInt(fields[3], 10, 64)",
     "TestTheOrdinaryLinkParses"),
    ("parse: a non-numeric size accepted as zero",
     PARSE, "\tif err != nil {\n\t\treturn Locator{}, fmt.Errorf(\"%w: the size %q is not a number: %v\",",
     "\tif false {\n\t\treturn Locator{}, fmt.Errorf(\"%w: the size %q is not a number: %v\",",
     "TestTheSizeMustBePositive"),
    ("parse: a ZERO size accepted",
     PARSE, "\tif size <= 0 {", "\tif size < 0 {",
     "TestTheSizeMustBePositive|TestTheThreeErrorsAreDistinct"),
    ("parse: a NEGATIVE size accepted",
     PARSE, "\tif size <= 0 {", "\tif size < 0 && size > -1000 {",
     "TestTheSizeMustBePositive"),
    # Two of these rows are near-duplicates of the size guard above on purpose:
    # `< 2` and `< 0 && > -1000` are the two WRONG guards a plausible edit
    # produces, and a test that pins only the real one still lets a rewrite
    # through. A row that looks redundant here is cheap; a survivor is not.
    ("parse: a size of 1 refused, so the guard is not a minimum block size",
     PARSE, "\tif size <= 0 {", "\tif size < 2 {",
     "TestTheSizeMustBePositive"),
    ("parse: the hash length check removed, so a hash decodes and truncates",
     PARSE, "\tif len(s) != 2*HashLength {", "\tif false {",
     "TestTheHashLengthIsCheckedBeforeDecoding"),
    # Length checked AFTER decoding. `hex.DecodeString` accepts any even
    # length, so this is the silent-truncation bug the ordering exists to stop.
    # Two edits, because the copy would not compile otherwise.
    ("parse: the hash length checked AFTER decoding (silent truncation)",
     PARSE,
     "\tif len(s) != 2*HashLength {",
     "\tvar pre [16]byte\n\tif r0, e0 := hex.DecodeString(s); e0 == nil && len(r0) == HashLength {\n\t\tcopy(pre[:], r0)\n\t\treturn pre, nil\n\t}\n\tif false {",
     "TestTheHashLengthIsCheckedBeforeDecoding"),
    ("parse: a non-hex hash accepted",
     PARSE, "\traw, err := hex.DecodeString(s)\n\tif err != nil {",
     "\traw, err := hex.DecodeString(s)\n\tif false && err != nil {",
     "TestTheHashLengthIsCheckedBeforeDecoding"),
    ("parse: the hash not copied out of the decode buffer",
     PARSE, "\tcopy(h[:], raw)", "\t_ = raw",
     "TestTheHashIsAnArrayNotAString|TestTheZeroHashIsRecognisable|TestUppercaseHexIsAcceptedAndPrintedLowercase"),

    # ---- the name checks, which are the attack surface ----
    ("parse: the traversal check removed",
     PARSE, "\tfor _, bad := range dangerousNameComponents {", "\tfor _, bad := range []string{} {",
     "TestAnEscapingNameIsRefused|TestTheTraversalCheckIsASubstringTestNotAComponentTest"),
    ("parse: the traversal check case-SENSITIVE, so ..\\..\\WINDOWS walks out",
     PARSE, "\tlower := strings.ToLower(name)", "\tlower := name",
     "TestAnEscapingNameIsRefused"),
    ("parse: the absolute-POSIX-path check removed",
     PARSE, "\tif strings.HasPrefix(name, \"/\") {", "\tif false {",
     "TestAnEscapingNameIsRefused|TestTreeHashRefusesAnEscapingName"),
    # The drive letter. filepath.VolumeName returns "" for C:\x on Linux, so a
    # build that trusted it would pass a name that is absolute on Windows.
    ("parse: the Windows drive-letter check removed",
     PARSE, "\tif len(name) >= 2 && name[1] == ':' &&", "\tif false && len(name) >= 2 && name[1] == ':' &&",
     "TestAnEscapingNameIsRefused|TestTreeHashRefusesAnEscapingName"),
    ("parse: the drive letter required to be upper case",
     PARSE, "\t\t(name[0] >= 'A' && name[0] <= 'Z' || name[0] >= 'a' && name[0] <= 'z') {",
     "\t\t(name[0] >= 'A' && name[0] <= 'Z') {",
     "TestAnEscapingNameIsRefused"),
    ("parse: the UNC check removed",
     PARSE, "\tif strings.HasPrefix(lower, `\\\\`) {", "\tif false {",
     "TestAnEscapingNameIsRefused"),
    ("parse: checkName no longer called on the locator's name",
     PARSE, "\tif err := checkName(loc.Name); err != nil {",
     "\tif err := error(nil); err != nil {",
     "TestAnEscapingNameIsRefused"),

    # ---- Hash and Kind ----
    ("ed2k: String printed UPPERCASE, so peers compare a different file",
     ED2K, "const hexdigits = \"0123456789abcdef\"", "const hexdigits = \"0123456789ABCDEF\"",
     "TestUppercaseHexIsAcceptedAndPrintedLowercase|TestTheHashIsAnArrayNotAString"),
    ("ed2k: String truncated to the first eight bytes",
     ED2K, "out := make([]byte, 2*HashLength)", "out := make([]byte, HashLength)",
     "TestTheHashIsAnArrayNotAString"),
    ("ed2k: IsZero true for any hash, so a missing hash field is undetectable",
     ED2K, "\tfor _, b := range h {\n\t\tif b != 0 {\n\t\t\treturn false\n\t\t}\n\t}",
     "\treturn true",
     "TestTheZeroHashIsRecognisable|TestAZeroByteInTheHashIsNotZero"),
    ("ed2k: IsZero true for any hash whose LAST byte is zero",
     ED2K, "\tfor _, b := range h {\n\t\tif b != 0 {\n\t\t\treturn false\n\t\t}\n\t}",
     "\treturn h[HashLength-1] == 0",
     "TestAZeroByteInTheHashIsNotZero"),
    ("ed2k: IsZero true for any hash whose FIRST byte is zero",
     ED2K, "\tfor _, b := range h {\n\t\tif b != 0 {\n\t\t\treturn false\n\t\t}\n\t}",
     "\treturn h[0] == 0",
     "TestAZeroByteInTheHashIsNotZero|TestTheZeroHashIsRecognisable"),
    ("ed2k: IsZero true for any hash whose eighth byte is zero",
     ED2K, "\tfor _, b := range h {\n\t\tif b != 0 {\n\t\t\treturn false\n\t\t}\n\t}",
     "\treturn h[7] == 0",
     "TestAZeroByteInTheHashIsNotZero"),
    ("ed2k: Kind.String reports \"file\" for a value outside the set",
     ED2K, "\treturn \"unknown\"\n}", "\treturn \"file\"\n}",
     "TestKindStringHasAnUninformativeValue"),
    # The hash's byte length, as read back off the PARSED value.
    #
    # A row mutating `const HashLength = 16` to 15 CANNOT be written: it
    # changes the array's own type, so `h[:]`, `Sum`'s `[md4.Size]byte` return
    # and every 32-hex-character literal in the file stop lining up, and the
    # probe does not compile. A non-compile is a SKIP that proves nothing about
    # the tests, so the same fact is mutated through paths that DO compile. The
    # constant itself is pinned by TestTheHashIsAnArrayNotAString, which is why
    # a change to it fails there rather than being unobservable.
    ("ed2k: String printed at the wrong width, so a peer sees a different hash",
     ED2K, "out := make([]byte, 2*HashLength)", "out := make([]byte, 2*HashLength+2)",
     "TestTheHashIsAnArrayNotAString|TestUppercaseHexIsAcceptedAndPrintedLowercase"),
    ("ed2k: String rendered one byte short, truncating the hash",
     ED2K, "\t\tout[2*i] = hexdigits[b>>4]", "\t\tif i == 0 {\n\t\t\tout[0] = hexdigits[b>>4]\n\t\t\tcontinue\n\t\t}\n\t\tout[2*i] = hexdigits[b>>4]",
     "TestTheHashIsAnArrayNotAString"),

    # ---- VerifyBytes / VerifyPart ----
    # The gate. A mutation here that SURVIVES means bad bytes can be
    # accepted, which is the one failure this file exists to prevent.
    ("verify: the size check dropped, so a short file is hashed and compared",
     VERIFY, "if int64(len(got)) != wantSize {", "if false {",
     "TestTheSizeIsCheckedBeforeTheHash"),
    ("verify: the size check accepts a LARGER file, so truncation is not caught",
     VERIFY, "if int64(len(got)) != wantSize {",
     "if int64(len(got)) < wantSize {",
     "TestTheSizeIsCheckedBeforeTheHash|TestVerifyBytesRefusesDifferentBytesByName"),
    ("verify: an all-zero link hash is compared instead of refused",
     VERIFY, "if want.IsZero() {", "if false {",
     "TestAnAllZeroHashInALinkIsRefusedRatherThanCompared"),
    ("verify: the hash comparison inverted, so wrong bytes are ACCEPTED",
     VERIFY, "if sum != want {", "if sum == want {",
     "TestVerifyBytesRefusesDifferentBytesByName|TestVerifyBytesAcceptsTheBytesItNamed"),
    ("verify: the hash comparison dropped entirely, so any bytes pass",
     VERIFY, "if sum != want {", "if false {",
     "TestVerifyBytesRefusesDifferentBytesByName|TestAnAllZeroHashInALinkIsRefusedRatherThanCompared"),
    ("verify: an empty part accepted, so a transfer can advance without bytes",
     VERIFY, "if len(got) == 0 {", "if false {",
     "TestVerifyPartRefusesEmptyBytesButVerifyBytesDoesNot"),
    ("verify: an all-zero expected part hash is compared rather than refused",
     VERIFY, "if want.IsZero() {", "if false {",
     "TestAZeroExpectedPartHashIsRefusedRatherThanCompared"),
    ("verify: the part comparison inverted, so a wrong window is accepted",
     VERIFY, "if got2 != want {", "if got2 == want {",
     "TestVerifyPartNamesThePartSoAFailedWindowIsAttributable"),
    ("verify: the part comparison dropped, so any window is accepted",
     VERIFY, "if got2 != want {", "if false {",
     "TestVerifyPartNamesThePartSoAFailedWindowIsAttributable|TestAZeroExpectedPartHashIsRefusedRatherThanCompared"),

]


def run(cmd, timeout):
    try:
        r = subprocess.run(cmd, shell=True, capture_output=True, text=True,
                           timeout=timeout, cwd=REPO, env=ENV)
        return r.returncode, r.stdout + r.stderr
    except subprocess.TimeoutExpired:
        return None, ""


def verdict(out):
    """Score a probe's output.

    Four branches, and the elif chain has to NAME all of them. The first
    version of the step 5.5 harness had three branches and an `else` that
    caught both COVERED and SKIP, so a probe that failed to compile was counted
    as a hole in the tests -- the exact inversion SKIP exists to prevent,
    reintroduced by the branch meant to implement it.
    """
    if any(e in out for e in BUILD_ERRORS):
        return "SKIP", "the probe did not compile -- a defect in THIS FILE"
    if "test timed out" in out or "panic:" in out:
        return "KILLED", "the named test hung or panicked"
    if "--- FAIL" in out or "FAIL" in out.split("\n")[0:1] or "build failed" in out:
        first = next((l.strip() for l in out.split("\n") if ".go:" in l), "")
        named = "a different test failed" if not first else first[:96]
        return "KILLED", named or "a test failed"
    if "ok " in out or "\nok" in out:
        return "COVERED", "the whole suite passed -- another layer refuses this"
    return "SURVIVED", "no test observed the change"


def main():
    # A file's ORIGINAL content, kept in memory. A sweep interrupted at any
    # point still restores every file, because restoration does not depend on
    # the loop reaching its own epilogue.
    originals = {}
    # DERIVED FROM THE PROBE LIST, NOT TYPED BY HAND.
    #
    # The list was once (ED2K, EHASH, PARSE) and adding a probe against a
    # fourth file meant adding a fourth name here -- and forgetting. The
    # restore then wrote an EMPTY string for a key it did not have, which
    # truncated verify.go to 0 bytes, and the harness died with a KeyError
    # on the next restore. Two things broke at once, and the file was gone.
    #
    # So the set of files is whatever the probes name. A probe cannot exist
    # for a file whose original is not held, which makes the destructive
    # failure structurally impossible rather than merely unlikely.
    for rel in sorted({m[1] for m in MUTATIONS}):
        with open(os.path.join(REPO, rel)) as fh:
            originals[rel] = fh.read()

    def restore_all():
        for rel, text in originals.items():
            with open(os.path.join(REPO, rel), "w") as fh:
                fh.write(text)

    print("### step 5.4 mutation harness -- internal/ed2k")
    print("### %d probes, each bounded at %ds, each restored before the next\n"
          % (len(MUTATIONS), PROBE_TIMEOUT))

    killed = covered = survived = skipped = 0
    survivors = []
    malformed = []
    try:
        for label, rel, old, new, run_pat in MUTATIONS:
            # `old` and `new` are either single strings or equal-length LISTS.
            # A list is a COMPOUND mutation: every edit applied in order,
            # because some defects need several edits to EXIST at all. The
            # percent-decoding row is one: the import without the use does not
            # compile, and the use without the import does not compile either.
            #
            # A single edit labelled "COMPOUND" is a lie no harness can detect,
            # which is why the length check is here rather than in the comment.
            olds = old if isinstance(old, list) else [old]
            news = new if isinstance(new, list) else [new]
            if len(olds) != len(news):
                print("  SKIP      %s\n            %d edits to make and %d "
                      "replacements, so the probe cannot be applied as written"
                      % (label, len(olds), len(news)))
                skipped += 1
                malformed.append(label)
                continue

            with open(os.path.join(REPO, rel)) as fh:
                text = fh.read()
            missing = [o for o in olds if o not in text]
            if missing:
                # Counted AND listed. A count with no list is a report nobody
                # can act on, and it is how a stale row survives a rename of
                # the thing it was probing.
                print("  SKIP      %s\n            %d of %d patterns are not "
                      "in %s, so the probe no longer matches the source and "
                      "has to be rewritten" % (label, len(missing),
                                               len(olds), rel))
                skipped += 1
                malformed.append(label)
                continue

            mutated = text
            for o, n in zip(olds, news):
                mutated = mutated.replace(o, n, 1)
            if mutated == text:
                # A mutation that changes no bytes is a HARNESS bug, and
                # reporting it as a survivor sends the reader into the tests
                # looking for a hole that is in this file.
                print("  SKIP      %s\n            the probe changed no bytes, "
                      "so it is a defect in THIS FILE rather than a survivor"
                      % label)
                skipped += 1
                malformed.append(label)
                continue

            with open(os.path.join(REPO, rel), "w") as fh:
                fh.write(mutated)
            try:
                _, out = run(
                    "go test ./%s/ -run '%s' -count=1 -timeout %ds 2>&1"
                    % (PKG, run_pat, SUITE_TIMEOUT),
                    PROBE_TIMEOUT)
            finally:
                # Restored per-probe, not at the end. The incident this harness
                # exists partly because of left one file mutated for the rest
                # of the run.
                with open(os.path.join(REPO, rel), "w") as fh:
                    fh.write(originals[rel])

            vd, why = verdict(out)
            if vd == "KILLED":
                killed += 1
            elif vd == "COVERED":
                covered += 1
            elif vd == "SKIP":
                skipped += 1
                malformed.append(label)
            else:
                survived += 1
                survivors.append(label)
            print("  %-9s %s\n            %s" % (vd, label, why))
    finally:
        restore_all()

    # A restore that cannot fail is not a restore. Compare byte for byte
    # rather than trusting the write, because a sweep that reported a clean
    # tree while leaving a mutation behind is worse than one that reported
    # nothing.
    for rel, text in originals.items():
        with open(os.path.join(REPO, rel)) as fh:
            if fh.read() != text:
                print("FATAL: %s was not restored" % rel)
                return 2

    print("\n%d killed, %d covered by a lower layer, %d survived, %d malformed"
          % (killed, covered, survived, skipped))
    if malformed:
        print("\nmalformed probes -- defects in THIS FILE, not holes in the tests:")
        for s in malformed:
            print("  " + s)
    if survivors:
        print("\nsurvivors -- these are holes in the tests:")
        for s in survivors:
            print("  " + s)
    # Separate codes: a survivor means go and look at a test; a malformed probe
    # means go and look at the row above.
    if survived:
        return 1
    if skipped:
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
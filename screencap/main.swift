// claude-remote-screencap captures one display with ScreenCaptureKit and
// encodes it to H.264 with VideoToolbox, for the server to relay over WebRTC.
//
//	claude-remote-screencap state
//	    Print {"locked":…, "desktop":…, "displays":[…], "cursor":{x,y}, "asleep":…} and exit.
//	claude-remote-screencap stream [--display builtin|main|<id>] [--width 1920]
//	                               [--fps 60] [--bitrate 6000000]
//	    stdout: one record per frame — 4-byte big-endian length, then the
//	            access unit as Annex-B NAL units (SPS/PPS before every IDR).
//	    stderr: JSON event lines: geometry (first), locked, error.
//	    stdin:  a "k" line asks for a keyframe; EOF stops the capture.
//
// Geometry is in global display points (the same space as CGDisplayBounds);
// the page maps it onto the Screen Sharing framebuffer, which covers the
// bounding box of all displays.
import CoreGraphics
import CoreMedia
import CoreVideo
import Foundation
import ScreenCaptureKit
import VideoToolbox

signal(SIGPIPE, SIG_IGN)

let stderrLock = NSLock()

func emit(_ obj: [String: Any]) {
    guard var data = try? JSONSerialization.data(withJSONObject: obj) else { return }
    data.append(0x0a)
    stderrLock.lock()
    FileHandle.standardError.write(data)
    stderrLock.unlock()
}

func fail(_ message: String, code: Int32 = 1) -> Never {
    emit(["event": "error", "message": message])
    exit(code)
}

func isLocked() -> Bool {
    guard let d = CGSessionCopyCurrentDictionary() as? [String: Any] else { return false }
    return (d["CGSSessionScreenIsLocked"] as? NSNumber)?.boolValue ?? false
}

struct Display {
    let id: CGDirectDisplayID
    let bounds: CGRect
    let builtin: Bool
    let main: Bool
    let pixelWidth: Int

    var json: [String: Any] {
        ["id": id, "builtin": builtin, "main": main, "pixelWidth": pixelWidth, "frame": rectJSON(bounds)]
    }
}

func rectJSON(_ r: CGRect) -> [String: Any] {
    ["x": r.origin.x, "y": r.origin.y, "w": r.width, "h": r.height]
}

func activeDisplays() -> [Display] {
    var ids = [CGDirectDisplayID](repeating: 0, count: 16)
    var n: UInt32 = 0
    guard CGGetActiveDisplayList(16, &ids, &n) == .success else { return [] }
    return ids[0..<Int(n)].map { id in
        let mode = CGDisplayCopyDisplayMode(id)
        return Display(id: id, bounds: CGDisplayBounds(id), builtin: CGDisplayIsBuiltin(id) != 0,
                       main: CGDisplayIsMain(id) != 0, pixelWidth: mode?.pixelWidth ?? CGDisplayPixelsWide(id))
    }
}

// A sleeping display drops out of the active list, so ask the online ones.
func displaysAsleep() -> Bool {
    var ids = [CGDirectDisplayID](repeating: 0, count: 16)
    var n: UInt32 = 0
    guard CGGetOnlineDisplayList(16, &ids, &n) == .success, n > 0 else { return false }
    return ids[0..<Int(n)].allSatisfy { CGDisplayIsAsleep($0) != 0 }
}

func desktopBounds(_ ds: [Display]) -> CGRect {
    ds.reduce(CGRect.null) { $0.union($1.bounds) }
}

// MARK: - Output

let stdoutQueue = DispatchQueue(label: "stdout")

func writeAll(_ data: Data) {
    data.withUnsafeBytes { (buf: UnsafeRawBufferPointer) in
        var off = 0
        while off < buf.count {
            let n = write(1, buf.baseAddress! + off, buf.count - off)
            if n < 0 {
                if errno == EINTR { continue }
                exit(0) // the server hung up
            }
            off += n
        }
    }
}

func writeFrame(_ au: Data) {
    var len = UInt32(au.count).bigEndian
    var rec = Data(bytes: &len, count: 4)
    rec.append(au)
    stdoutQueue.async { writeAll(rec) }
}

// MARK: - Encoder

final class Encoder: @unchecked Sendable {
    private var session: VTCompressionSession!
    private let lock = NSLock()
    private var forceKey = true
    private var last: CVPixelBuffer?
    private(set) var lastEncode = Date.distantPast
    private let start = CMClockGetTime(CMClockGetHostTimeClock())

    init(width: Int, height: Int, fps: Int, bitrate: Int) {
        let spec: [CFString: Any] = [kVTVideoEncoderSpecification_EnableLowLatencyRateControl: true]
        var s: VTCompressionSession?
        let err = VTCompressionSessionCreate(
            allocator: nil, width: Int32(width), height: Int32(height), codecType: kCMVideoCodecType_H264,
            encoderSpecification: spec as CFDictionary, imageBufferAttributes: nil,
            compressedDataAllocator: nil, outputCallback: nil, refcon: nil, compressionSessionOut: &s)
        guard err == noErr, let s else { fail("VTCompressionSessionCreate: \(err)") }
        session = s
        // Constrained Baseline has no B-frames, so every frame decodes the
        // moment it arrives, and every WebRTC browser can play it.
        if VTSessionSetProperty(s, key: kVTCompressionPropertyKey_ProfileLevel,
                                value: kVTProfileLevel_H264_ConstrainedBaseline_AutoLevel) != noErr {
            VTSessionSetProperty(s, key: kVTCompressionPropertyKey_ProfileLevel,
                                 value: kVTProfileLevel_H264_Baseline_AutoLevel)
        }
        VTSessionSetProperty(s, key: kVTCompressionPropertyKey_RealTime, value: kCFBooleanTrue)
        VTSessionSetProperty(s, key: kVTCompressionPropertyKey_AllowFrameReordering, value: kCFBooleanFalse)
        VTSessionSetProperty(s, key: kVTCompressionPropertyKey_MaxKeyFrameIntervalDuration, value: 2 as CFNumber)
        VTSessionSetProperty(s, key: kVTCompressionPropertyKey_MaxKeyFrameInterval, value: (fps * 2) as CFNumber)
        VTSessionSetProperty(s, key: kVTCompressionPropertyKey_ExpectedFrameRate, value: fps as CFNumber)
        VTSessionSetProperty(s, key: kVTCompressionPropertyKey_AverageBitRate, value: bitrate as CFNumber)
        VTCompressionSessionPrepareToEncodeFrames(s)
    }

    func requestKeyframe() {
        lock.lock()
        forceKey = true
        let idle = Date().timeIntervalSince(lastEncode) > 0.1
        lock.unlock()
        // A still screen produces no frames, so a requested keyframe would
        // never come; re-encode the last picture instead.
        if idle { repeatLast() }
    }

    func repeatLast() {
        lock.lock()
        let pb = last
        lock.unlock()
        if let pb { encode(pb) }
    }

    func encode(_ pb: CVPixelBuffer) {
        lock.lock()
        last = pb
        let key = forceKey
        forceKey = false
        lastEncode = Date()
        lock.unlock()
        let pts = CMTimeSubtract(CMClockGetTime(CMClockGetHostTimeClock()), start)
        let props = key ? [kVTEncodeFrameOptionKey_ForceKeyFrame: true] as CFDictionary : nil
        VTCompressionSessionEncodeFrame(session, imageBuffer: pb, presentationTimeStamp: pts,
                                        duration: .invalid, frameProperties: props, infoFlagsOut: nil) { status, _, sbuf in
            if status == noErr, let sbuf { Encoder.output(sbuf) }
        }
    }

    // VideoToolbox emits AVCC (length-prefixed NALs); WebRTC wants Annex-B.
    static func output(_ sbuf: CMSampleBuffer) {
        guard let bb = CMSampleBufferGetDataBuffer(sbuf),
              let fmt = CMSampleBufferGetFormatDescription(sbuf) else { return }
        let startCode: [UInt8] = [0, 0, 0, 1]
        var au = Data()
        let attachments = CMSampleBufferGetSampleAttachmentsArray(sbuf, createIfNecessary: false) as? [[CFString: Any]]
        let notSync = (attachments?.first?[kCMSampleAttachmentKey_NotSync] as? Bool) ?? false

        var count = 0
        var headerLen: Int32 = 4
        CMVideoFormatDescriptionGetH264ParameterSetAtIndex(
            fmt, parameterSetIndex: 0, parameterSetPointerOut: nil, parameterSetSizeOut: nil,
            parameterSetCountOut: &count, nalUnitHeaderLengthOut: &headerLen)
        if !notSync {
            for i in 0..<count {
                var p: UnsafePointer<UInt8>?
                var size = 0
                CMVideoFormatDescriptionGetH264ParameterSetAtIndex(
                    fmt, parameterSetIndex: i, parameterSetPointerOut: &p, parameterSetSizeOut: &size,
                    parameterSetCountOut: nil, nalUnitHeaderLengthOut: nil)
                if let p {
                    au.append(contentsOf: startCode)
                    au.append(p, count: size)
                }
            }
        }

        let total = CMBlockBufferGetDataLength(bb)
        var avcc = Data(count: total)
        let copied = avcc.withUnsafeMutableBytes { CMBlockBufferCopyDataBytes(bb, atOffset: 0, dataLength: total, destination: $0.baseAddress!) }
        guard copied == kCMBlockBufferNoErr else { return }
        let hl = Int(headerLen)
        var off = 0
        while off + hl <= total {
            var n = 0
            for i in 0..<hl { n = n << 8 | Int(avcc[off + i]) }
            off += hl
            guard n > 0, off + n <= total else { break }
            au.append(contentsOf: startCode)
            au.append(avcc[off..<off + n])
            off += n
        }
        writeFrame(au)
    }
}

// MARK: - Capture

final class Capturer: NSObject, SCStreamOutput, SCStreamDelegate {
    let encoder: Encoder
    init(encoder: Encoder) { self.encoder = encoder }

    func stream(_ stream: SCStream, didOutputSampleBuffer sb: CMSampleBuffer, of type: SCStreamOutputType) {
        guard type == .screen,
              let infos = CMSampleBufferGetSampleAttachmentsArray(sb, createIfNecessary: false) as? [[SCStreamFrameInfo: Any]],
              let raw = infos.first?[.status] as? Int,
              SCFrameStatus(rawValue: raw) == .complete,
              let pb = CMSampleBufferGetImageBuffer(sb) else { return }
        encoder.encode(pb)
    }

    func stream(_ stream: SCStream, didStopWithError error: Error) {
        emit(["event": "error", "message": "capture stopped: \(error.localizedDescription)", "locked": isLocked()])
        exit(3)
    }
}

func pickDisplay(_ ds: [Display], _ want: String) -> Display? {
    switch want {
    case "", "builtin": return ds.first(where: \.builtin) ?? ds.first(where: \.main) ?? ds.first
    case "main": return ds.first(where: \.main) ?? ds.first
    // Display IDs can change after a reboot or re-plug: show the built-in one
    // rather than nothing.
    default: return ds.first(where: { String($0.id) == want }) ?? pickDisplay(ds, "builtin")
    }
}

func even(_ v: Double) -> Int { max(2, Int(v.rounded()) & ~1) }

var capturer: Capturer?
var scStream: SCStream?

func stream(args: [String: String]) async {
    let width = Int(args["width"] ?? "") ?? 1920
    let fps = Int(args["fps"] ?? "") ?? 60
    let bitrate = Int(args["bitrate"] ?? "") ?? 6_000_000

    let ds = activeDisplays()
    guard let d = pickDisplay(ds, args["display"] ?? "") else { fail("no such display") }
    // Never upscale past the panel's real resolution.
    let outW = even(Double(min(width, d.pixelWidth)))
    let outH = even(Double(outW) * d.bounds.height / d.bounds.width)
    emit(["event": "geometry", "display": d.json, "desktop": rectJSON(desktopBounds(ds)),
          "displays": ds.map(\.json), "width": outW, "height": outH])

    if !CGPreflightScreenCaptureAccess() {
        CGRequestScreenCaptureAccess()
        fail("Screen Recording permission is missing: allow claude-remote in System Settings → Privacy & Security → Screen & System Audio Recording", code: 2)
    }

    var lockedNow = isLocked()
    emit(["event": "locked", "locked": lockedNow])
    if lockedNow { fail("the Mac is locked", code: 4) }

    let content: SCShareableContent
    do {
        content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
    } catch {
        fail("ScreenCaptureKit: \(error.localizedDescription)", code: 2)
    }
    guard let scd = content.displays.first(where: { $0.displayID == d.id }) else { fail("display \(d.id) not capturable") }

    let enc = Encoder(width: outW, height: outH, fps: fps, bitrate: bitrate)
    let cfg = SCStreamConfiguration()
    cfg.width = outW
    cfg.height = outH
    cfg.minimumFrameInterval = CMTime(value: 1, timescale: CMTimeScale(fps))
    cfg.pixelFormat = kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange
    cfg.colorMatrix = CGDisplayStream.yCbCrMatrix_ITU_R_709_2
    cfg.showsCursor = true
    cfg.queueDepth = 5
    let cap = Capturer(encoder: enc)
    let st = SCStream(filter: SCContentFilter(display: scd, excludingWindows: []), configuration: cfg, delegate: cap)
    do {
        try st.addStreamOutput(cap, type: .screen, sampleHandlerQueue: DispatchQueue(label: "frames", qos: .userInteractive))
        try await st.startCapture()
    } catch {
        fail("start capture: \(error.localizedDescription)", code: 2)
    }
    capturer = cap
    scStream = st

    // Keyframe requests from the server; EOF means it is gone.
    Thread.detachNewThread {
        while let line = readLine() {
            if line.hasPrefix("k") { enc.requestKeyframe() }
        }
        exit(0)
    }

    // ScreenCaptureKit sends nothing while the screen is still, so repeat the
    // last frame now and then: a new viewer, or one that lost a packet, gets
    // a picture without waiting for something to move.
    let timer = DispatchSource.makeTimerSource(queue: DispatchQueue(label: "tick"))
    timer.schedule(deadline: .now() + 0.5, repeating: 0.5)
    timer.setEventHandler {
        let l = isLocked()
        if l != lockedNow {
            lockedNow = l
            emit(["event": "locked", "locked": l])
        }
        if Date().timeIntervalSince(enc.lastEncode) > 1 { enc.repeatLast() }
    }
    timer.resume()
    keepAlive.append(timer)
}

var keepAlive: [AnyObject] = []

func parseFlags(_ argv: ArraySlice<String>) -> [String: String] {
    var out: [String: String] = [:]
    var it = argv.makeIterator()
    while let a = it.next() {
        guard a.hasPrefix("--") else { continue }
        out[String(a.dropFirst(2))] = it.next() ?? ""
    }
    return out
}

let argv = CommandLine.arguments
switch argv.count > 1 ? argv[1] : "" {
case "state":
    let ds = activeDisplays()
    var obj: [String: Any] = ["locked": isLocked(), "desktop": rectJSON(desktopBounds(ds)), "displays": ds.map(\.json)]
    // The pointer in global points: the page moves it through Screen Sharing
    // and reads it back here to learn how framebuffer pixels map to displays.
    if let p = CGEvent(source: nil)?.location { obj["cursor"] = ["x": p.x, "y": p.y] }
    // Keys typed while the display is still waking are lost, so the page
    // waits for this to turn false before typing a password.
    obj["asleep"] = displaysAsleep()
    let data = try! JSONSerialization.data(withJSONObject: obj)
    FileHandle.standardOutput.write(data)
    print()
case "stream":
    let flags = parseFlags(argv.dropFirst(2))
    Task { await stream(args: flags) }
    dispatchMain()
default:
    FileHandle.standardError.write("usage: claude-remote-screencap state | stream [--display builtin|main|ID] [--width N] [--fps N] [--bitrate N]\n".data(using: .utf8)!)
    exit(64)
}

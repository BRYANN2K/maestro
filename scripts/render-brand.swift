import AppKit
let args = CommandLine.arguments
guard args.count == 3 else {
    FileHandle.standardError.write(Data("Usage: swift scripts/render-brand.swift INPUT.svg OUTPUT.png\n".utf8))
    exit(2)
}
let input = args[1], output = args[2]
guard let image = NSImage(contentsOfFile: input) else { fatalError("Cannot load SVG") }
let width = Int(image.size.width), height = Int(image.size.height)
guard let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: width, pixelsHigh: height, bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0), let context = NSGraphicsContext(bitmapImageRep: bitmap) else { fatalError("Cannot create image context") }
NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = context
image.draw(in: NSRect(x: 0, y: 0, width: width, height: height))
NSGraphicsContext.restoreGraphicsState()
guard let data = bitmap.representation(using: .png, properties: [:]) else { fatalError("Cannot encode PNG") }
try data.write(to: URL(fileURLWithPath: output))
print("Rendered \(output) (\(width)x\(height))")

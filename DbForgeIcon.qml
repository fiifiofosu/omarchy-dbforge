import QtQuick
import qs.Commons

// The database mark, drawn as three stacked discs rather than pulled from a
// Nerd Font. At bar sizes a font glyph depends on which Nerd Font the user's
// theme happened to resolve to and renders at whatever weight that font chose;
// drawing it keeps the icon identical across themes and lets it take the bar's
// colour directly. At this size a rounded rectangle reads as an ellipse, so
// there are no arcs to get wrong.
Item {
  id: root

  property real iconSize: Style.font.icon
  property color color: Color.foreground

  width: iconSize
  height: iconSize
  implicitWidth: iconSize
  implicitHeight: iconSize

  readonly property real discWidth: width * 0.84
  readonly property real discHeight: height * 0.26
  readonly property real step: discHeight + (height - discHeight * 3) / 2

  // Three discs rather than a Repeater: the fading opacity differs per disc,
  // so a delegate would be indexing into a table of three constants.
  Rectangle {
    width: root.discWidth; height: root.discHeight
    x: (root.width - width) / 2; y: 0
    radius: height / 2
    color: root.color
    antialiasing: true
  }

  // The lower discs read as the shaded underside of a cylinder.
  Rectangle {
    width: root.discWidth; height: root.discHeight
    x: (root.width - width) / 2; y: root.step
    radius: height / 2
    color: root.color
    opacity: 0.75
    antialiasing: true
  }

  Rectangle {
    width: root.discWidth; height: root.discHeight
    x: (root.width - width) / 2; y: root.step * 2
    radius: height / 2
    color: root.color
    opacity: 0.5
    antialiasing: true
  }
}

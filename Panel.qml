import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import Quickshell
import Quickshell.Io
import qs.Commons
import qs.Ui
import "Model.js" as Model

// The bar widget and its panel in one component, which is the contract the
// Omarchy plugin guide describes for `bar-widget`: the manifest points at this
// file, and the panel is loaded from inside it rather than declared as a
// second plugin kind. Panel (qs.Ui) already supplies open/close/toggle/
// closeForPopoutSwitch and the opened/popoutSwitchClosing properties the bar
// reads back, so this file only adds behaviour.
Panel {
  id: root
  moduleName: "io.github.fiifiofosu.dbforge"
  ipcTarget: "io.github.fiifiofosu.dbforge"
  manageIpc: false

  // One flat cursor over the whole panel: the optional action row first, then
  // one row per instance. Two independent section indices would have to be
  // kept in sync every time the daemon's answer changes shape, and the list
  // does change shape -- it collapses to a single row the moment dbforged
  // goes away.
  property int cursor: 0
  property bool cursorActive: false

  readonly property color foreground: bar ? bar.foreground : Color.foreground
  readonly property color urgent: bar ? bar.urgent : Color.urgent
  readonly property color dim: Qt.darker(foreground, 1.55)
  readonly property string fontFamily: bar ? bar.fontFamily : Style.font.family

  readonly property bool hasActionRow: !db.missingBinary && (db.offline || db.instances.length === 0)
  readonly property int actionRows: hasActionRow ? 1 : 0
  readonly property int rowCount: actionRows + db.instances.length

  readonly property string actionTitle: db.offline ? "Start the DBForge daemon" : "No instances yet"
  readonly property string actionDetail: db.offline
    ? "systemctl --user start dbforged"
    : "Open DBForge to create your first database"

  readonly property color barIconColor: {
    if (db.summary.problems > 0) return barForeground
    if (db.summary.running > 0) return barForeground
    return Qt.darker(barForeground, 1.55)
  }

  readonly property string tooltip: {
    if (db.missingBinary) return "DBForge: dbctl not found"
    if (db.offline) return "DBForge: daemon not running"
    return "DBForge: " + Model.summaryText(db.summary, false)
  }

  function selectedInstance() {
    var i = cursor - actionRows
    if (i < 0 || i >= db.instances.length) return null
    return db.instances[i]
  }

  function clampCursor() {
    if (rowCount === 0) { cursor = 0; return }
    if (cursor < 0) cursor = 0
    if (cursor > rowCount - 1) cursor = rowCount - 1
  }

  function moveCursor(dx, dy) {
    cursorActive = true
    if (dy === 0) return
    cursor = cursor + dy
    clampCursor()
    scrollCursorIntoView()
  }

  function setCursor(index) {
    cursorActive = true
    cursor = index
    clampCursor()
  }

  function activateCursor() {
    if (cursor < actionRows && hasActionRow) {
      if (db.offline) db.startDaemon()
      else db.openTui()
      return
    }
    db.toggleInstance(selectedInstance())
  }

  function copySelected() {
    db.copyConnString(selectedInstance())
  }

  function scrollItemIntoView(item) {
    if (!panelFlick || !item) return
    Qt.callLater(function() {
      if (!item) return
      var margin = Style.space(6)
      var point = item.mapToItem(panelFlick.contentItem, 0, 0)
      var top = point.y
      var bottom = top + item.height
      var viewTop = panelFlick.contentY
      var viewBottom = viewTop + panelFlick.height
      var maxY = Math.max(0, panelFlick.contentHeight - panelFlick.height)
      if (top < viewTop + margin) panelFlick.contentY = Math.max(0, top - margin)
      else if (bottom > viewBottom - margin) panelFlick.contentY = Math.min(maxY, bottom + margin - panelFlick.height)
    })
  }

  function scrollCursorIntoView() {
    var i = cursor - actionRows
    if (i >= 0 && rowColumn && i < rowColumn.children.length) scrollItemIntoView(rowColumn.children[i])
    else if (panelFlick) panelFlick.contentY = 0
  }

  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight

  onOpenedChanged: if (opened) {
    cursorActive = false
    cursor = 0
    if (panelFlick) panelFlick.contentY = 0
    db.refresh()
    Qt.callLater(function() { keyCatcher.forceActiveFocus() })
  }

  Service {
    id: db
    settings: root.settings
  }

  Connections {
    target: db
    // The list is rebuilt on every poll, so a cursor parked on the last row
    // would point past the end as soon as an instance is destroyed elsewhere.
    function onInstancesChanged() { root.clampCursor() }
  }

  IpcHandler {
    target: root.ipcTarget
    function open(): void { root.open() }
    function close(): void { root.close() }
    function show(): void { root.open() }
    function hide(): void { root.close() }
    function toggle(): void { root.toggle() }
    function refresh(): string { db.refresh(); return "ok" }
    function status(): string { return Model.summaryText(db.summary, db.offline) }
  }

  BarIconButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    slotSize: Style.bar.statusSlot
    tooltipText: root.tooltip
    active: db.summary.problems > 0
    iconComponent: Component {
      Item {
        DbForgeIcon {
          anchors.centerIn: parent
          iconSize: Style.space(13)
          color: button.active ? button.activeColor : root.barIconColor
          opacity: db.summary.running > 0 || db.summary.problems > 0 ? 1.0 : 0.6
        }
      }
    }

    onPressed: function(buttonCode) {
      if (buttonCode === Qt.RightButton) db.refresh()
      else if (buttonCode === Qt.MiddleButton) db.openTui()
      else root.toggle()
    }
  }

  KeyboardPanel {
    id: panel
    anchorItem: button
    owner: root
    bar: root.bar
    open: root.opened
    focusTarget: keyCatcher
    contentWidth: panel.fittedContentWidth(Style.space(400))
    contentHeight: panel.fittedContentHeight(column.implicitHeight, Style.space(520))

    PanelKeyCatcher {
      id: keyCatcher
      anchors.fill: parent
      onMoveRequested: function(dx, dy) {
        if (!root.cursorActive) { root.cursorActive = true; return }
        root.moveCursor(dx, dy)
      }
      onActivateRequested: if (root.cursorActive) root.activateCursor()
      onCloseRequested: root.close()
      onTabRequested: function(direction) { root.switchPanel(direction) }
      onTextKey: function(t) {
        var key = String(t || "").toLowerCase()
        if (key === "r") db.refresh()
        else if (key === "c") root.copySelected()
        else if (key === "t") db.openTui()
      }

      Flickable {
        id: panelFlick
        anchors.fill: parent
        contentWidth: width
        contentHeight: column.implicitHeight
        clip: true
        boundsBehavior: Flickable.StopAtBounds
        flickableDirection: Flickable.VerticalFlick
        interactive: contentHeight > height
        ScrollBar.vertical: ScrollBar { policy: ScrollBar.AsNeeded }

        Column {
          id: column
          width: panelFlick.width
          spacing: Style.space(12)

          PanelHero {
            id: hero
            width: parent.width
            title: "DBForge"
            meta: db.missingBinary
              ? "dbctl not found"
              : Model.summaryText(db.summary, db.offline)
            foreground: root.foreground
            fontFamily: root.fontFamily
            iconOpacity: db.offline || db.missingBinary ? 0.5 : 1.0
            iconComponent: Component {
              DbForgeIcon {
                iconSize: Style.font.display
                color: db.summary.problems > 0 ? root.urgent : root.foreground
              }
            }

            // The terminal button is the panel's escape hatch: creating and
            // destroying instances is destructive enough to belong in the TUI,
            // where it can confirm, rather than one keystroke away in the bar.
            trailingControl: Component {
              PanelActionButton {
                iconText: "󰆍"
                foreground: hero.foreground
                fontFamily: hero.fontFamily
                tooltipText: "Open the DBForge TUI"
                enabled: db.tuiPath !== ""
                onClicked: db.openTui()
              }
            }
          }

          Text {
            textFormat: Text.PlainText
            visible: text !== ""
            width: parent.width
            text: db.actionStatus !== "" ? db.actionStatus : db.lastError
            color: db.actionStatus === "" && db.lastError !== "" ? root.urgent : root.dim
            font.family: root.fontFamily
            font.pixelSize: Style.font.bodySmall
            wrapMode: Text.WordWrap
          }

          // dbctl missing is not something the panel can fix, so it explains
          // rather than offering a button that would fail.
          Text {
            textFormat: Text.PlainText
            visible: db.missingBinary
            width: parent.width
            text: "Install DBForge, or set the dbctl path in this widget's settings."
            color: root.dim
            font.family: root.fontFamily
            font.pixelSize: Style.font.bodySmall
            wrapMode: Text.WordWrap
          }

          ActionRow {
            visible: root.hasActionRow
            width: parent.width
          }

          PanelSeparator {
            visible: db.instances.length > 0
            foreground: root.foreground
          }

          Column {
            visible: db.instances.length > 0
            width: parent.width
            spacing: Style.space(10)

            PanelSectionHeader {
              text: "INSTANCES"
              foreground: root.foreground
              fontFamily: root.fontFamily
            }

            Column {
              id: rowColumn
              width: parent.width
              spacing: Style.space(6)

              Repeater {
                model: db.instances

                InstanceRow {
                  required property var modelData
                  required property int index
                  width: rowColumn.width
                  instance: modelData
                  rowIndex: index
                }
              }
            }
          }
        }
      }
    }
  }

  component ActionRow: CursorSurface {
    id: actionRow

    hasCursor: root.cursorActive && root.hasActionRow && root.cursor === 0
    foreground: root.foreground
    implicitHeight: actionContent.implicitHeight + Style.spacing.rowPaddingX

    MouseArea {
      anchors.fill: parent
      hoverEnabled: true
      cursorShape: Qt.PointingHandCursor
      onEntered: root.setCursor(0)
      onClicked: root.activateCursor()
    }

    RowLayout {
      anchors.left: parent.left
      anchors.right: parent.right
      anchors.verticalCenter: parent.verticalCenter
      anchors.leftMargin: Style.space(10)
      anchors.rightMargin: Style.space(10)
      spacing: Style.space(8)

      Text {
        textFormat: Text.PlainText
        text: db.offline ? "󰐊" : "󰐕"
        color: root.foreground
        font.family: root.fontFamily
        font.pixelSize: Style.font.icon
        Layout.alignment: Qt.AlignVCenter
      }

      ColumnLayout {
        id: actionContent
        Layout.fillWidth: true
        spacing: Style.space(1)

        Text {
          textFormat: Text.PlainText
          Layout.fillWidth: true
          text: root.actionTitle
          color: root.foreground
          font.family: root.fontFamily
          font.pixelSize: Style.font.body
          elide: Text.ElideRight
        }

        Text {
          textFormat: Text.PlainText
          Layout.fillWidth: true
          text: root.actionDetail
          color: root.dim
          font.family: root.fontFamily
          font.pixelSize: Style.font.caption
          elide: Text.ElideRight
        }
      }
    }
  }

  component InstanceRow: CursorSurface {
    id: instanceRow
    property var instance: null
    property int rowIndex: 0

    readonly property bool selected: root.cursorActive && root.cursor === root.actionRows + rowIndex
    readonly property bool broken: Model.severity(instance) >= 2

    hasCursor: selected
    foreground: root.foreground
    implicitHeight: rowContent.implicitHeight + Style.spacing.rowPaddingX

    MouseArea {
      anchors.fill: parent
      hoverEnabled: true
      cursorShape: Qt.PointingHandCursor
      onEntered: root.setCursor(root.actionRows + instanceRow.rowIndex)
      onClicked: db.copyConnString(instanceRow.instance)
    }

    RowLayout {
      anchors.left: parent.left
      anchors.right: parent.right
      anchors.verticalCenter: parent.verticalCenter
      anchors.leftMargin: Style.space(10)
      anchors.rightMargin: Style.space(10)
      spacing: Style.space(8)

      ColumnLayout {
        id: rowContent
        Layout.fillWidth: true
        spacing: Style.space(1)

        Text {
          textFormat: Text.PlainText
          Layout.fillWidth: true
          text: instanceRow.instance ? instanceRow.instance.id : ""
          color: root.foreground
          font.family: root.fontFamily
          font.pixelSize: Style.font.body
          elide: Text.ElideRight
        }

        Text {
          textFormat: Text.PlainText
          Layout.fillWidth: true
          text: Model.rowMeta(instanceRow.instance)
          color: instanceRow.broken ? root.urgent : root.dim
          font.family: root.fontFamily
          font.pixelSize: Style.font.caption
          elide: Text.ElideRight
        }
      }

      PanelActionButton {
        iconText: "󰆏"
        foreground: root.foreground
        fontFamily: root.fontFamily
        tooltipText: "Copy connection string"
        Layout.alignment: Qt.AlignVCenter
        onClicked: db.copyConnString(instanceRow.instance)
      }

      // A missing container has drifted out from under the daemon, so there is
      // nothing here to switch on: the row states the problem and leaves the
      // repair to the TUI, which can explain it.
      ToggleSwitch {
        visible: Model.isActionable(instanceRow.instance)
        checked: Model.isRunning(instanceRow.instance)
        busy: db.busy
        hasCursor: instanceRow.selected
        foreground: root.foreground
        Layout.alignment: Qt.AlignVCenter
        onHovered: function(on) { if (on) root.setCursor(root.actionRows + instanceRow.rowIndex) }
        onToggled: db.toggleInstance(instanceRow.instance)
      }
    }
  }
}

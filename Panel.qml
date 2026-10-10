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

  // The inline create form. Its engine and version live here rather than in
  // the dropdowns because the version list depends on the engine, and the
  // dropdowns' own `value` stops following a binding once a user picks.
  property bool createOpen: false
  property SafeTextField pasteTarget: null
  property string createEngine: ""
  property string createVersion: ""
  readonly property var createVersions: versionsFor(createEngine)
  readonly property bool canCreate: db.ready && !db.offline && !db.creating

  readonly property color foreground: bar ? bar.foreground : Color.foreground
  readonly property color urgent: bar ? bar.urgent : Color.urgent
  readonly property color dim: Qt.darker(foreground, 1.55)
  readonly property string fontFamily: bar ? bar.fontFamily : Style.font.family

  readonly property bool hasActionRow: db.missingBinary
    || db.offline || (db.instances.length === 0 && !createOpen)
  readonly property int actionRows: hasActionRow ? 1 : 0
  readonly property int rowCount: actionRows + db.instances.length

  readonly property string actionTitle: db.missingBinary
    ? "Install DBForge"
    : (db.offline ? "Start the DBForge daemon" : "No instances yet")
  readonly property string actionDetail: db.missingBinary
    ? "Opens a terminal and sets up everything it needs"
    : (db.offline
      ? "systemctl --user start dbforged"
      : "Press n, or click here, to create your first database")

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
      if (db.missingBinary) db.runInstaller()
      else if (db.offline) db.startDaemon()
      else openCreate()
      return
    }
    db.toggleInstance(selectedInstance())
  }

  function copySelected() {
    db.copyConnString(selectedInstance())
  }

  function versionsFor(name) {
    for (var i = 0; i < db.engines.length; i++) {
      if (db.engines[i].name === name) return db.engines[i].versions
    }
    return []
  }

  // Keeps whatever engine was picked last time; otherwise Postgres, which is
  // what most people reach for, or failing that the first engine listed.
  function pickDefaultEngine() {
    if (versionsFor(createEngine).length > 0 || db.engines.length === 0) return
    var name = db.engines[0].name
    for (var i = 0; i < db.engines.length; i++) {
      if (db.engines[i].name === "postgres") name = "postgres"
    }
    setCreateEngine(name)
  }

  function setCreateEngine(name) {
    createEngine = name
    var versions = versionsFor(name)
    createVersion = versions.length > 0 ? versions[0] : ""
    engineDropdown.value = createEngine
    versionDropdown.value = createVersion
  }

  function openCreate() {
    if (createOpen) { nameField.forceActiveFocus(); return }
    if (!canCreate) return
    if (db.engines.length === 0) db.loadEngines()
    pickDefaultEngine()
    nameField.text = ""
    versionField.text = ""
    createOpen = true
    Qt.callLater(function() { nameField.forceActiveFocus() })
  }

  function closeCreate() {
    createOpen = false
    keyCatcher.forceActiveFocus()
  }

  function submitCreate() {
    var version = createVersions.length > 0 ? createVersion : versionField.text.trim()
    if (!canCreate || createEngine === "" || version === "") return
    db.create(createEngine + ":" + version, nameField.text.trim())
    closeCreate()
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

  onCreateOpenChanged: clampCursor()

  onOpenedChanged: if (!opened) {
    db.actionError = ""
  } else {
    cursorActive = false
    cursor = 0
    createOpen = false
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
    // The engine list can arrive after the form has opened.
    function onEnginesChanged() { root.pickDefaultEngine() }
    function onClipboardRead(text) {
      if (root.pasteTarget && root.createOpen) root.pasteTarget.insertPasted(text)
      root.pasteTarget = null
    }
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
    contentHeight: panel.fittedContentHeight(
      header.implicitHeight + panelFlick.anchors.topMargin + rowColumn.implicitHeight,
      Style.space(520))

    PanelKeyCatcher {
      id: keyCatcher
      anchors.fill: parent
      // While the form's fields or dropdowns own the keyboard, j/k/n/Esc
      // belong to them and not to the panel cursor.
      blocked: nameField.activeFocus || versionField.activeFocus
        || engineDropdown.popupOpen || versionDropdown.popupOpen
      onMoveRequested: function(dx, dy) {
        if (!root.cursorActive) { root.cursorActive = true; return }
        root.moveCursor(dx, dy)
      }
      onActivateRequested: if (root.cursorActive) root.activateCursor()
      onCloseRequested: root.createOpen ? root.closeCreate() : root.close()
      onTabRequested: function(direction) { root.switchPanel(direction) }
      onTextKey: function(t) {
        var key = String(t || "").toLowerCase()
        if (key === "r") db.refresh()
        else if (key === "c") root.copySelected()
        else if (key === "t") db.openTui()
        else if (key === "n") root.openCreate()
      }

      // Everything above the instance list stays put; only the list scrolls.
      // With a dozen instances the hero and its buttons would otherwise
      // scroll out of reach along with the rows.
      Column {
        id: header
        anchors.top: parent.top
        anchors.left: parent.left
        anchors.right: parent.right
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

          // Creating is offered here because it is additive: the worst a
          // mistaken create costs is a stopped container. Destroying stays
          // in the TUI, where it can confirm and explain what is lost.
          trailingControl: Component {
            Row {
              spacing: Style.space(4)

              PanelActionButton {
                iconText: root.createOpen ? "󰅖" : "󰐕"
                foreground: hero.foreground
                fontFamily: hero.fontFamily
                tooltipText: root.createOpen ? "Cancel" : "New instance (n)"
                enabled: root.canCreate || root.createOpen
                onClicked: root.createOpen ? root.closeCreate() : root.openCreate()
              }

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
        }

        Text {
          readonly property string errorText: db.actionError !== "" ? db.actionError : db.lastError
          textFormat: Text.PlainText
          visible: text !== ""
          width: parent.width
          text: db.actionStatus !== "" ? db.actionStatus : errorText
          color: db.actionStatus === "" && errorText !== "" ? root.urgent : root.dim
          font.family: root.fontFamily
          font.pixelSize: Style.font.bodySmall
          wrapMode: Text.WordWrap
        }

        ActionRow {
          visible: root.hasActionRow
          width: parent.width
        }

        // The install row covers the common case; this covers the user who
        // has dbctl somewhere the search does not look.
        Text {
          textFormat: Text.PlainText
          visible: db.missingBinary
          width: parent.width
          text: "Installed somewhere else? Set the dbctl path in this widget's settings."
          color: root.dim
          font.family: root.fontFamily
          font.pixelSize: Style.font.bodySmall
          wrapMode: Text.WordWrap
        }

        Column {
          id: createForm
          visible: root.createOpen
          width: parent.width
          spacing: Style.space(8)

          PanelSectionHeader {
            text: "NEW INSTANCE"
            foreground: root.foreground
            fontFamily: root.fontFamily
          }

          RowLayout {
            width: parent.width
            spacing: Style.space(8)

            Dropdown {
              id: engineDropdown
              showLabel: false
              Layout.fillWidth: true
              Layout.preferredWidth: 1
              options: db.engines.map(function(e) { return e.name })
              foreground: root.foreground
              fontFamily: root.fontFamily
              onChanged: function(value) {
                root.setCreateEngine(value)
                nameField.forceActiveFocus()
              }
            }

            Dropdown {
              id: versionDropdown
              visible: root.createVersions.length > 0
              showLabel: false
              Layout.fillWidth: true
              Layout.preferredWidth: 1
              options: root.createVersions
              foreground: root.foreground
              fontFamily: root.fontFamily
              onChanged: function(value) {
                root.createVersion = value
                nameField.forceActiveFocus()
              }
            }

            // An older dbctl names its engines but not their versions, so
            // the version is typed instead; dbforged rejects any it has not
            // pinned, and that error lands in the status line.
            SafeTextField {
              id: versionField
              allowed: /[0-9A-Za-z.]/
              maximumLength: 16
              visible: root.createVersions.length === 0
              Layout.fillWidth: true
              Layout.preferredWidth: 1
              foreground: root.foreground
              placeholderText: "version, e.g. 16"
              onAccepted: root.submitCreate()
              Keys.onEscapePressed: root.closeCreate()
            }
          }

          // Same character set and length dbforged's validateID accepts, so
          // a bad name is refused as it is typed rather than after a create.
          SafeTextField {
            id: nameField
            allowed: /[A-Za-z0-9_-]/
            maximumLength: 48
            width: parent.width
            foreground: root.foreground
            placeholderText: root.createEngine === ""
              ? "name"
              : "name (default " + Model.defaultInstanceId(root.createEngine,
                  root.createVersions.length > 0 ? root.createVersion : versionField.text.trim()) + ")"
            onAccepted: root.submitCreate()
            Keys.onEscapePressed: root.closeCreate()
          }

          RowLayout {
            width: parent.width
            spacing: Style.space(8)

            Text {
              textFormat: Text.PlainText
              Layout.fillWidth: true
              text: "Enter to create · Esc to cancel"
              color: root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.caption
              elide: Text.ElideRight
            }

            Button {
              text: "Cancel"
              foreground: root.foreground
              fontFamily: root.fontFamily
              onClicked: root.closeCreate()
            }

            Button {
              text: "Create"
              bordered: true
              foreground: root.foreground
              fontFamily: root.fontFamily
              onClicked: root.submitCreate()
            }
          }
        }

        PanelSeparator {
          visible: db.instances.length > 0
          foreground: root.foreground
        }

        PanelSectionHeader {
          visible: db.instances.length > 0
          text: "INSTANCES"
          foreground: root.foreground
          fontFamily: root.fontFamily
        }
      }

      Flickable {
        id: panelFlick
        anchors.top: header.bottom
        anchors.topMargin: db.instances.length > 0 ? Style.space(10) : 0
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.bottom: parent.bottom
        contentWidth: width
        contentHeight: rowColumn.implicitHeight
        clip: true
        boundsBehavior: Flickable.StopAtBounds
        flickableDirection: Flickable.VerticalFlick
        interactive: contentHeight > height
        ScrollBar.vertical: ScrollBar { policy: ScrollBar.AsNeeded }

        Column {
          id: rowColumn
          width: panelFlick.width
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

  // A TextField whose clipboard never touches the shell's own thread.
  //
  // Qt's native paste (Ctrl+V, Shift+Insert, middle-click on the primary
  // selection, and the right-click menu Qt 6.9+ adds) reads the Wayland
  // clipboard synchronously inside the shell process, with no deadline and
  // no size limit -- a source that keeps streaming would stall every panel
  // and grow the shell's memory. So all four are closed off here, and the
  // keyboard paste goes through Service.readClipboard instead: wl-paste in a
  // child process, cut off after a second and a few hundred bytes. What comes
  // back is filtered to `allowed` and trimmed to maximumLength before it is
  // inserted, so a paste can never put text in the field that typing could
  // not.
  component SafeTextField: TextField {
    id: field
    property var allowed: /./

    function insertPasted(raw) {
      var clean = ""
      var text = String(raw || "")
      for (var i = 0; i < text.length; i++) {
        if (allowed.test(text[i])) clean += text[i]
      }
      if (selectedText !== "") remove(selectionStart, selectionEnd)
      clean = clean.substring(0, Math.max(0, maximumLength - length))
      if (clean !== "") insert(cursorPosition, clean)
    }

    validator: RegularExpressionValidator {
      regularExpression: new RegExp("^" + field.allowed.source + "*$")
    }
    foreground: root.foreground
    ContextMenu.menu: null

    Keys.onPressed: function(event) {
      if (event.matches(StandardKey.Paste)) {
        root.pasteTarget = field
        db.readClipboard()
        event.accepted = true
      }
    }

    // Middle-click would paste the primary selection, right-click opens the
    // menu with Paste in it. Taking both buttons here leaves left-click
    // (focus, cursor, selection) to the field itself.
    MouseArea {
      anchors.fill: parent
      acceptedButtons: Qt.MiddleButton | Qt.RightButton
      onPressed: field.forceActiveFocus()
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
        text: db.missingBinary ? "󰇚" : (db.offline ? "󰐊" : "󰐕")
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

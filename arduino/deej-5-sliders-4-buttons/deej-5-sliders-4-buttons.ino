// deej with buttons.
//
// Buttons are sent as extra channels on the same line as the sliders, after
// them: with 5 sliders and 4 buttons the line is "s0|s1|s2|s3|s4|b0|b1|b2|b3",
// so the buttons are channels 5 to 8. A pressed button reports 1023, a released
// one reports 0, which is why the existing line format needs no changes at all.
//
// Channel order follows buttonInputs below, not physical position: whichever
// switch is on D2 is channel 5. If the mapping comes out shuffled relative to
// how they sit in the case, reorder this array rather than rewiring.
//
// Wiring (no resistors needed): connect one leg of a momentary push button to
// the digital pin, the other leg to GND. INPUT_PULLUP holds the pin high while
// the button is open and it reads LOW when pressed, so we invert below.
//
// Debouncing happens here rather than on the PC side. The Arduino is where the
// timing authority lives, and doing it here means the serial stream never
// carries a bounce for anything downstream to have to filter out.

const int NUM_SLIDERS = 5;
const int NUM_BUTTONS = 4;

const int analogInputs[NUM_SLIDERS] = {A0, A1, A2, A3, A4};
const int buttonInputs[NUM_BUTTONS] = {2, 3, 4, 5};

// how long a button reading has to hold steady before we believe it. mechanical
// contacts bounce for a few milliseconds after every press and release
const unsigned long DEBOUNCE_MS = 25;

int analogSliderValues[NUM_SLIDERS];

bool buttonStableStates[NUM_BUTTONS];
bool buttonLastReadings[NUM_BUTTONS];
unsigned long buttonLastChangeMs[NUM_BUTTONS];

void setup() {
  for (int i = 0; i < NUM_SLIDERS; i++) {
    pinMode(analogInputs[i], INPUT);
  }

  for (int i = 0; i < NUM_BUTTONS; i++) {
    pinMode(buttonInputs[i], INPUT_PULLUP);

    buttonStableStates[i] = false;
    buttonLastReadings[i] = false;
    buttonLastChangeMs[i] = 0;
  }

  // 115200 rather than deej's usual 9600. A full line is up to 46 bytes, which
  // takes ~48ms at 9600 - longer than the loop - so println blocked on a full
  // transmit buffer and the loop really ran every 30-50ms. Buttons were only
  // sampled that often, a quick tap could fall between samples, and the 25ms
  // debounce amounted to "same reading twice". baud_rate in config.yaml has to
  // match.
  Serial.begin(115200);
}

void loop() {
  updateSliderValues();
  updateButtonStates();
  sendValues(); // Actually send data (all the time)
  // printValues(); // For debug
  delay(10);
}

void updateSliderValues() {
  for (int i = 0; i < NUM_SLIDERS; i++) {
     analogSliderValues[i] = analogRead(analogInputs[i]);
  }
}

void updateButtonStates() {
  unsigned long now = millis();

  for (int i = 0; i < NUM_BUTTONS; i++) {
    // INPUT_PULLUP means the pin reads LOW while the button is held down
    bool reading = (digitalRead(buttonInputs[i]) == LOW);

    // any change restarts the settling clock - while the contact is bouncing
    // this keeps happening and the stable state is left alone
    if (reading != buttonLastReadings[i]) {
      buttonLastReadings[i] = reading;
      buttonLastChangeMs[i] = now;
    }

    // once it's held steady for long enough, accept it
    if ((now - buttonLastChangeMs[i]) >= DEBOUNCE_MS) {
      buttonStableStates[i] = reading;
    }
  }
}

void sendValues() {
  // built in a fixed buffer rather than with String concatenation, which
  // reallocates on the AVR's 2KB heap every loop. 9 values of up to 4 digits
  // plus separators is 44 characters
  char line[64];
  int length = 0;

  for (int i = 0; i < NUM_SLIDERS; i++) {
    length += snprintf(line + length, sizeof(line) - length, "%d|", analogSliderValues[i]);
  }

  for (int i = 0; i < NUM_BUTTONS; i++) {
    length += snprintf(line + length, sizeof(line) - length,
                       i < NUM_BUTTONS - 1 ? "%d|" : "%d",
                       buttonStableStates[i] ? 1023 : 0);
  }

  Serial.println(line);
}

void printValues() {
  for (int i = 0; i < NUM_SLIDERS; i++) {
    String printedString = String("Slider #") + String(i + 1) + String(": ") + String(analogSliderValues[i]) + String(" mV");
    Serial.write(printedString.c_str());
    Serial.write(" | ");
  }

  for (int i = 0; i < NUM_BUTTONS; i++) {
    String printedString = String("Button #") + String(i + 1) + String(": ") + String(buttonStableStates[i] ? "down" : "up");
    Serial.write(printedString.c_str());

    if (i < NUM_BUTTONS - 1) {
      Serial.write(" | ");
    } else {
      Serial.write("\n");
    }
  }
}

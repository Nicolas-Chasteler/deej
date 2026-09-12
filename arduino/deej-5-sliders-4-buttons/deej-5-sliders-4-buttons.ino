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

  Serial.begin(9600);
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
  String builtString = String("");

  for (int i = 0; i < NUM_SLIDERS; i++) {
    builtString += String((int)analogSliderValues[i]);
    builtString += String("|");
  }

  for (int i = 0; i < NUM_BUTTONS; i++) {
    builtString += String(buttonStableStates[i] ? 1023 : 0);

    if (i < NUM_BUTTONS - 1) {
      builtString += String("|");
    }
  }

  Serial.println(builtString);
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

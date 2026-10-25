#ifndef TEXTINPUT_H
#define TEXTINPUT_H

#import <Foundation/Foundation.h>

typedef void (*TextInputQueryCallback)(const char *query, void *userData);
typedef void (*TextInputControlCallback)(void *userData);

/// Start the hint search field. tabCallback may be NULL, which leaves Tab to
/// the field; when set, Tab with no marked text calls it instead of typing.
int NeruStartHintSearchTextInput(
    TextInputQueryCallback queryCallback, TextInputControlCallback confirmCallback,
    TextInputControlCallback cancelCallback, TextInputControlCallback tabCallback, int x, int y, int width, int height,
    void *userData);

void NeruStopHintSearchTextInput(void);

#endif

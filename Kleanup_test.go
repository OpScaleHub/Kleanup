--- a/Kleanup_test.go
+++ b/Kleanup_test.go
@@ -154,17 +154,22 @@
 	for _, tt := range tests {
 		t.Run(tt.name, func(t *testing.T) {
 			// Create a copy to avoid modifying the input map directly in the test definition
-			metadataCopy := make(map[string]interface{})
-			for k, v := range tt.inputMetadata {
-				metadataCopy[k] = v // Shallow copy is okay here
-			}
-
-			cleaner.Clean(metadataCopy, tt.options)
+			// Create a dummy object to pass to the cleaner
+			obj := &KubernetesObject{
+				// Kind might be needed if state preservation logic affects metadata directly
+				// For these specific tests, it might not matter, but good practice
+				Kind: "TestKind", // Use a placeholder kind
+				Metadata: make(map[string]interface{}),
+			}
+			if tt.inputMetadata != nil {
+				for k, v := range tt.inputMetadata {
+					obj.Metadata[k] = v // Shallow copy is okay here
+				}
+			}
+
+			cleaner.Clean(obj, tt.options)

 			// Special handling for the nil case when RemoveEmpty is true
-			if tt.expectedOutput == nil {
-				if len(metadataCopy) != 0 {
-					t.Errorf("Expected metadata to be empty, but got: %v", metadataCopy)
-				}
-			} else if !reflect.DeepEqual(tt.expectedOutput, metadataCopy) {
-				t.Errorf("Metadata not cleaned correctly.\nExpected: %v\nActual:   %v", tt.expectedOutput, metadataCopy)
+			// Note: The cleaner itself doesn't set obj.Metadata to nil if empty, removeEmptyFields does that later.
+			// So we compare the potentially non-nil but empty map.
+			if !reflect.DeepEqual(tt.expectedOutput, obj.Metadata) {
+				// Handle expected nil vs actual empty map case for better error message
+				if tt.expectedOutput == nil && len(obj.Metadata) == 0 {
+					// This is considered equal for the purpose of this test after cleaning
+				} else {
+					t.Errorf("Metadata not cleaned correctly.\nExpected: %v\nActual:   %v", tt.expectedOutput, obj.Metadata)
+				}
 			}
 		})
 	}

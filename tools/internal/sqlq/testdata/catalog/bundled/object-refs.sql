/* References of an object.

    Parameter: @name - the object.
*/
SELECT TOP (5) name FROM sys.objects WHERE name = @name;
